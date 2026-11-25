package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
	"github.com/Max252t/T-Travels/backend/internal/receipts"
	"github.com/Max252t/T-Travels/backend/internal/trips"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTripsPostgres(t *testing.T) {
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is unset")
	}
	pool, err := pgxpool.New(t.Context(), databaseURL)
	if err != nil {
		t.Fatal("could not configure test PostgreSQL connection")
	}
	t.Cleanup(pool.Close)
	var users, tripIDs []string
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, table := range []string{"notifications", "settlements", "invitations", "expense_disputes", "expense_splits", "receipts", "expenses", "trip_members"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE trip_id = ANY($1::text[]::uuid[])", tripIDs); err != nil {
				t.Errorf("could not clean up %s: %v", table, err)
			}
		}
		if _, err := pool.Exec(ctx, "DELETE FROM trips WHERE id = ANY($1::text[]::uuid[])", tripIDs); err != nil {
			t.Error("could not clean up test trips", err)
		}
		for _, table := range []string{"idempotency_keys", "refresh_sessions"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE user_id = ANY($1::text[]::uuid[])", users); err != nil {
				t.Errorf("could not clean up %s: %v", table, err)
			}
		}
		if _, err := pool.Exec(ctx, "DELETE FROM users WHERE id = ANY($1::text[]::uuid[])", users); err != nil {
			t.Error("could not clean up test users", err)
		}
	})
	authService := auth.NewService(pool, []byte(rand.Text()+rand.Text()))
	register := func(name string) string {
		t.Helper()
		email, password := rand.Text()+"@example.test", "a long test passphrase"
		user, err := authService.Register(t.Context(), email, password, name)
		if err != nil {
			t.Fatal(err)
		}
		users = append(users, user.ID)
		tokens, err := authService.Login(t.Context(), email, password)
		if err != nil {
			t.Fatal(err)
		}
		return tokens.AccessToken
	}
	owner, member, outsider := register("Организатор"), register("Участник"), register("Гость")
	storageEndpoint := os.Getenv("TEST_RECEIPTS_ENDPOINT")
	const receiptBucket = "t-travels-test-receipts"
	var storage *receipts.Store
	if storageEndpoint != "" {
		storage, err = receipts.New(storageEndpoint, os.Getenv("RECEIPTS_S3_ACCESS_KEY"), os.Getenv("RECEIPTS_S3_SECRET_KEY"), receiptBucket, "us-east-1")
		if err != nil {
			t.Fatal(err)
		}
		if err := storage.CheckBucket(t.Context(), true); err != nil {
			t.Fatal(err)
		}
	}
	tripService := trips.NewService(pool, storage)
	api := newRouter(authService, tripService)
	contract := loadContract(t, api)
	type result struct {
		request  *http.Request
		response *httptest.ResponseRecorder
	}
	call := func(method, path, token string, key int, body string) result {
		request := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if token != "" {
			request.Header.Set("Authorization", "Bearer "+token)
		}
		if key > 0 {
			request.Header.Set("Idempotency-Key", fmt.Sprintf("%08x-0000-4000-8000-000000000001", key))
		}
		if body != "" {
			request.Header.Set("Content-Type", "application/json")
		}
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return result{request, response}
	}
	check := func(got result, status int) []byte {
		t.Helper()
		if got.response.Code != status {
			t.Fatalf("%s %s returned %d, want %d: %s", got.request.Method, got.request.URL.Path, got.response.Code, status, got.response.Body)
		}
		validateResponse(t, contract, got.request, got.response)
		return got.response.Body.Bytes()
	}
	decode := func(raw []byte, target any) {
		t.Helper()
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	check(call("GET", "/trips", "", 0, ""), 401)
	if string(check(call("GET", "/trips", owner, 0, ""), 200)) != "[]" {
		t.Fatal("expected an empty list")
	}
	const create = `{"name":"Поездка","destination":"Омск","start_date":"2026-12-10","end_date":"2026-12-12","budget":40000}`
	check(call("POST", "/trips", owner, 0, create), 400)
	concurrent := make(chan result, 2)
	for range 2 {
		go func() { concurrent <- call("POST", "/trips", owner, 1, create) }()
	}
	var trip, replay trips.Trip
	decode(check(<-concurrent, 201), &trip)
	tripIDs = append(tripIDs, trip.ID)
	decode(check(<-concurrent, 201), &replay)
	if trip != replay || trip.Role != "owner" || trip.Currency != "RUB" || trip.Version != 1 {
		t.Fatal("trip creation was not idempotent")
	}
	var listed []trips.Trip
	decode(check(call("GET", "/trips?limit=1", owner, 0, ""), 200), &listed)
	if len(listed) != 1 || listed[0] != trip {
		t.Fatal("wrong owner trip list")
	}
	check(call("POST", "/trips", owner, 1, strings.Replace(create, "40000", "1", 1)), 409)
	path := "/trips/" + trip.ID
	check(call("GET", path, outsider, 0, ""), 404)
	check(call("GET", path+"/members", outsider, 0, ""), 404)
	check(call("POST", path+"/invitations", outsider, 2, ""), 404)
	check(call("PATCH", path, owner, 2, `{"version":1,"budget":null}`), 400)
	check(call("PATCH", path, owner, 2, `{"version":1,"end_date":"2026-12-01"}`), 400)
	const patch = `{"version":1,"budget":0}`
	var updated trips.Trip
	decode(check(call("PATCH", path, owner, 2, patch), 200), &updated)
	if updated.Version != 2 || updated.Budget != 0 || updated.Name != trip.Name {
		t.Fatal("PATCH did not preserve omitted fields")
	}
	decode(check(call("PATCH", path, owner, 2, patch), 200), &replay)
	if replay != updated {
		t.Fatal("PATCH retry returned a different result")
	}
	check(call("PATCH", path, owner, 3, patch), 409)
	var invitation, invitationReplay trips.Invitation
	decode(check(call("POST", path+"/invitations", owner, 3, ""), 201), &invitation)
	decode(check(call("POST", path+"/invitations", owner, 3, ""), 201), &invitationReplay)
	if invitation != invitationReplay {
		t.Fatal("invitation retry returned a different code")
	}
	join := fmt.Sprintf(`{"code":%q}`, strings.ToLower(invitation.Code))
	var joined trips.Trip
	decode(check(call("POST", "/trips/join", member, 1, join), 200), &joined)
	if joined.Role != "member" || joined.ID != trip.ID {
		t.Fatal("wrong joined trip")
	}
	check(call("POST", "/trips/join", member, 1, join), 200)
	check(call("POST", "/trips/join", member, 2, join), 200)
	check(call("GET", path, member, 0, ""), 200)
	check(call("PATCH", path, member, 3, `{"version":2,"budget":1}`), 403)
	check(call("POST", path+"/invitations", member, 3, ""), 403)
	var members []trips.Member
	decode(check(call("GET", path+"/members", member, 0, ""), 200), &members)
	if len(members) != 2 || members[0].Role != "owner" || members[1].UserID != users[1] {
		t.Fatal("wrong membership list")
	}
	var notifications []trips.Notification
	decode(check(call("GET", "/notifications", owner, 0, ""), 200), &notifications)
	if len(notifications) != 1 || notifications[0].ActorID != users[1] || notifications[0].ReadAt != nil {
		t.Fatal("wrong join notifications")
	}
	notificationPath := "/notifications/" + notifications[0].ID + "/read"
	check(call("PATCH", notificationPath, outsider, 0, ""), 404)
	check(call("PATCH", notificationPath, owner, 0, ""), 204)
	check(call("POST", "/notifications/read-all", owner, 4, ""), 204)
	if _, err := pool.Exec(t.Context(), "UPDATE invitations SET expires_at = now() - interval '1 second' WHERE id = $1::text::uuid", invitation.ID); err != nil {
		t.Fatal(err)
	}
	check(call("POST", "/trips/join", outsider, 1, join), 404)
	check(call("POST", "/trips/join", member, 1, join), 200)
	decode(check(call("GET", "/notifications", owner, 0, ""), 200), &notifications)
	if len(notifications) != 1 || notifications[0].ReadAt == nil {
		t.Fatal("notification was not marked read")
	}

	expensesPath := path + "/expenses"
	budgetPath := path + "/budget"
	check(call("GET", budgetPath, outsider, 0, ""), 404)
	check(call("GET", expensesPath, outsider, 0, ""), 404)
	checkBudget := func(spent, remaining int64) {
		t.Helper()
		var budget trips.Budget
		decode(check(call("GET", budgetPath, member, 0, ""), 200), &budget)
		if budget.Budget != 0 || budget.Spent != spent || budget.Remaining != remaining || budget.Currency != "RUB" {
			t.Fatalf("incorrect budget: %+v", budget)
		}
	}
	checkBudget(0, 0)
	expenseBody := fmt.Sprintf(`{"title":"Обед","amount":101,"category":"food","payer_id":%q,"split_type":"equal","participants":[{"user_id":%q}],"date":"2026-12-10","description":"Чек позже"}`, users[0], users[1])
	check(call("POST", expensesPath, owner, 5, strings.Replace(expenseBody, users[1], users[2], 1)), 400)
	for range 2 {
		go func() { concurrent <- call("POST", expensesPath, owner, 5, expenseBody) }()
	}
	var expense trips.Expense
	createdExpense := string(check(<-concurrent, 201))
	decode([]byte(createdExpense), &expense)
	if string(check(<-concurrent, 201)) != createdExpense || expense.PayerID != users[0] || expense.CreatedBy != users[0] ||
		expense.Version != 1 || len(expense.Participants) != 1 || expense.Participants[0].UserID != users[1] || expense.Participants[0].Amount != 101 {
		t.Fatal("expense creation failed to preserve the payer or shares on retry")
	}
	checkBudget(101, -101)
	check(call("POST", expensesPath, owner, 6, strings.Replace(expenseBody, "101", "1000000000000", 1)), 409)
	expensePath := expensesPath + "/" + expense.ID
	check(call("GET", expensePath, outsider, 0, ""), 404)
	if string(check(call("GET", expensePath, member, 0, ""), 200)) != createdExpense {
		t.Fatal("stored expense changed")
	}
	check(call("PATCH", expensePath, member, 3, `{"version":1,"amount":115}`), 403)
	patchExpense := fmt.Sprintf(`{"version":1,"amount":115,"split_type":"exact","participants":[{"user_id":%q,"amount":115}],"description":null}`, users[1])
	updatedExpense := string(check(call("PATCH", expensePath, owner, 6, patchExpense), 200))
	decode([]byte(updatedExpense), &expense)
	if expense.Version != 2 || expense.Description != nil || len(expense.Participants) != 1 || expense.Participants[0].Amount != 115 ||
		string(check(call("PATCH", expensePath, owner, 6, patchExpense), 200)) != updatedExpense {
		t.Fatal("expense update was not atomic and idempotent")
	}
	check(call("PATCH", expensePath, owner, 7, `{"version":2,"amount":116}`), 400)
	check(call("PATCH", expensePath, owner, 7, `{"version":1,"amount":116}`), 409)
	checkBudget(115, -115)
	check(call("DELETE", expensePath+"?version=1", owner, 7, ""), 409)
	check(call("DELETE", expensePath+"?version=2", owner, 7, ""), 204)
	check(call("DELETE", expensePath+"?version=2", owner, 7, ""), 204)
	check(call("GET", expensePath, member, 0, ""), 404)
	if string(check(call("GET", expensesPath, member, 0, ""), 200)) != "[]" {
		t.Fatal("deleted expense was listed")
	}
	checkBudget(0, 0)
	decode(check(call("GET", "/notifications", member, 0, ""), 200), &notifications)
	if len(notifications) != 3 {
		t.Fatalf("expected one notification per expense change, got %d", len(notifications))
	}
	for _, event := range notifications {
		if event.ExpenseID == nil || *event.ExpenseID != expense.ID {
			t.Fatal("notification refers to a different expense")
		}
	}
	percentageBody := fmt.Sprintf(`{"title":"Такси","amount":8,"category":"transport","payer_id":%q,"split_type":"percentage","participants":[{"user_id":%q,"basis_points":7500},{"user_id":%q,"basis_points":2500}],"date":"2026-12-11"}`, users[0], users[0], users[1])
	decode(check(call("POST", expensesPath, member, 3, percentageBody), 201), &expense)
	if expense.CreatedBy != users[1] || expense.PayerID != users[0] {
		t.Fatal("payer and creator should be independent")
	}
	percentagePath := expensesPath + "/" + expense.ID
	decode(check(call("PATCH", percentagePath, member, 4, `{"version":1,"amount":12}`), 200), &expense)
	wantShares := map[string]int64{users[0]: 9, users[1]: 3}
	for _, share := range expense.Participants {
		if share.Amount != wantShares[share.UserID] || share.BasisPoints == nil {
			t.Fatal("percentage shares were not recalculated using stored basis points")
		}
	}
	if len(expense.Participants) != 2 {
		t.Fatal("percentage split changed the participant list")
	}
	checkBudget(12, -12)

	balancesPath := path + "/balances"
	settlementsPath := path + "/settlements"
	suggestionsPath := settlementsPath + "/suggestions"
	check(call("GET", balancesPath, outsider, 0, ""), 404)
	check(call("GET", suggestionsPath, outsider, 0, ""), 404)
	checkBalances := func(ownerAmount, memberAmount int64) {
		t.Helper()
		var balances []trips.Balance
		decode(check(call("GET", balancesPath, member, 0, ""), 200), &balances)
		if len(balances) != 2 {
			t.Fatalf("expected two balances, got %d", len(balances))
		}
		want := map[string]int64{users[0]: ownerAmount, users[1]: memberAmount}
		for _, balance := range balances {
			amount, exists := want[balance.UserID]
			if !exists || !balance.Active || balance.Balance != amount {
				t.Fatalf("incorrect balance: %+v", balance)
			}
		}
	}
	checkBalances(3, -3)
	leavePath := path + "/leave"
	removePath := path + "/members/" + users[1]
	checkCode := func(response result, status int, code string) {
		t.Helper()
		var payload struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		decode(check(response, status), &payload)
		if payload.Error.Code != code {
			t.Fatalf("got error code %q, want %q", payload.Error.Code, code)
		}
	}
	checkCode(call("POST", leavePath, member, 19, ""), 409, "NONZERO_MEMBER_BALANCE")
	checkCode(call("DELETE", removePath, owner, 19, ""), 409, "NONZERO_MEMBER_BALANCE")
	checkCode(call("POST", leavePath, owner, 19, ""), 409, "TRIP_OWNER_REQUIRED")
	check(call("DELETE", removePath, member, 19, ""), 403)
	disputePath := percentagePath + "/dispute"
	const reason = `{"reason":"Проверить оплату такси"}`
	check(call("POST", disputePath, outsider, 20, reason), 404)
	openedJSON := string(check(call("POST", disputePath, member, 20, reason), 201))
	var opened trips.ExpenseDispute
	decode([]byte(openedJSON), &opened)
	if opened.CreatedBy != users[1] || opened.ExpenseID != expense.ID || opened.ResolvedAt != nil ||
		string(check(call("POST", disputePath, member, 20, reason), 201)) != openedJSON {
		t.Fatal("incorrect dispute or replay")
	}
	checkCode(call("POST", disputePath, owner, 20, reason), 409, "EXPENSE_DISPUTE_OPEN")
	checkCode(call("POST", disputePath+"/resolve", owner, 20, ""), 403, "DISPUTE_AUTHOR_REQUIRED")
	checkCode(call("DELETE", percentagePath+"?version=2", owner, 20, ""), 409, "EXPENSE_DISPUTE_OPEN")
	var disputedExpense trips.Expense
	decode(check(call("GET", percentagePath, owner, 0, ""), 200), &disputedExpense)
	if disputedExpense.Dispute == nil || disputedExpense.Dispute.ID != opened.ID {
		t.Fatal("expense does not expose its dispute")
	}
	checkBalances(3, -3)
	checkBudget(12, -12)
	readSuggestions := func(amount int64) trips.Suggestions {
		t.Helper()
		var suggestions trips.Suggestions
		decode(check(call("GET", suggestionsPath, member, 0, ""), 200), &suggestions)
		if !suggestions.Optimal || suggestions.Revision < 1 {
			t.Fatal("invalid suggestions metadata")
		}
		if amount == 0 && len(suggestions.Transfers) != 0 {
			t.Fatal("zero balances should have no transfers")
		}
		if amount > 0 && (len(suggestions.Transfers) != 1 || suggestions.Transfers[0] != (trips.Transfer{FromUserID: users[1], ToUserID: users[0], Amount: amount})) {
			t.Fatalf("incorrect transfer suggestions: %+v", suggestions)
		}
		return suggestions
	}
	suggestions := readSuggestions(3)
	if !suggestions.HasOpenDisputes {
		t.Fatal("settlement suggestions did not report the open dispute")
	}
	checkCode(call("POST", settlementsPath, member, 9,
		fmt.Sprintf(`{"to_user_id":%q,"amount":2,"revision":%d}`, users[0], suggestions.Revision)), 409, "DISPUTE_ACKNOWLEDGEMENT_REQUIRED")
	settleBody := func(to string, amount, revision int64) string {
		return fmt.Sprintf(`{"to_user_id":%q,"amount":%d,"revision":%d,"acknowledge_disputes":true}`, to, amount, revision)
	}
	check(call("POST", settlementsPath, outsider, 9, settleBody(users[0], 1, suggestions.Revision)), 404)
	check(call("POST", settlementsPath, owner, 9, settleBody(users[1], 1, suggestions.Revision)), 409)
	check(call("POST", settlementsPath, member, 9, settleBody(users[0], 4, suggestions.Revision)), 409)
	check(call("PATCH", percentagePath, member, 5, `{"version":2,"description":"Поездка на вокзал"}`), 200)
	check(call("POST", settlementsPath, member, 9, settleBody(users[0], 1, suggestions.Revision)), 409)
	suggestions = readSuggestions(3)
	partial := settleBody(users[0], 2, suggestions.Revision)
	type keyedResult struct {
		key    int
		result result
	}
	competing := make(chan keyedResult, 2)
	for _, key := range []int{9, 10} {
		go func() { competing <- keyedResult{key, call("POST", settlementsPath, member, key, partial)} }()
	}
	var recorded trips.Settlement
	var wonKey int
	for range 2 {
		attempt := <-competing
		if attempt.result.response.Code == 201 {
			if wonKey != 0 {
				t.Fatal("both concurrent transfers succeeded")
			}
			wonKey = attempt.key
			raw := check(attempt.result, 201)
			decode(raw, &recorded)
		} else {
			check(attempt.result, 409)
		}
	}
	if wonKey == 0 || recorded.FromUserID != users[1] || recorded.ToUserID != users[0] || recorded.Amount != 2 {
		t.Fatalf("wrong simulated transfer: %+v", recorded)
	}
	var retried trips.Settlement
	decode(check(call("POST", settlementsPath, member, wonKey, partial), 201), &retried)
	if recorded != retried {
		t.Fatal("replay returned a different transfer")
	}
	check(call("POST", settlementsPath, member, wonKey, settleBody(users[0], 1, suggestions.Revision)), 409)
	checkBalances(1, -1)
	suggestions = readSuggestions(1)
	check(call("POST", settlementsPath, member, 11, settleBody(users[0], 1, suggestions.Revision)), 201)
	checkBalances(0, 0)
	readSuggestions(0)
	checkBudget(12, -12)
	var history []trips.Settlement
	decode(check(call("GET", settlementsPath, owner, 0, ""), 200), &history)
	if len(history) != 2 || history[1] != recorded || history[0].Amount != 1 {
		t.Fatal("incorrect transfer history")
	}
	notifications = nil
	decode(check(call("GET", "/notifications", owner, 0, ""), 200), &notifications)
	var settlementEvents int
	for _, event := range notifications {
		if event.EventType == "settlement_completed" {
			if event.SettlementID == nil {
				t.Fatal("settlement notification has no settlement id")
			}
			settlementEvents++
		}
	}
	if settlementEvents != 2 {
		t.Fatalf("expected two settlement notifications, got %d", settlementEvents)
	}
	checkCode(call("POST", leavePath, member, 19, ""), 409, "MEMBER_DISPUTE_OPEN")
	checkCode(call("DELETE", removePath, owner, 19, ""), 409, "MEMBER_DISPUTE_OPEN")
	resolvedJSON := string(check(call("POST", disputePath+"/resolve", member, 21, ""), 200))
	var resolved trips.ExpenseDispute
	decode([]byte(resolvedJSON), &resolved)
	if resolved.ID != opened.ID || resolved.ResolvedAt == nil ||
		string(check(call("POST", disputePath+"/resolve", member, 21, ""), 200)) != resolvedJSON {
		t.Fatal("resolution was not saved and replayed")
	}
	if readSuggestions(0).HasOpenDisputes {
		t.Fatal("resolved dispute still affects the warning")
	}
	var resolvedCount int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM expense_disputes WHERE expense_id = $1::text::uuid AND resolved_at IS NOT NULL`, expense.ID).
		Scan(&resolvedCount); err != nil || resolvedCount != 1 {
		t.Fatal("dispute history was not preserved")
	}

	check(call("POST", leavePath, member, 19, ""), 204)
	check(call("POST", leavePath, member, 19, ""), 204)
	check(call("GET", percentagePath, member, 0, ""), 404)
	checkCode(call("PATCH", percentagePath, owner, 22, `{"version":3,"amount":16}`), 409, "INACTIVE_MEMBER_BALANCE")
	checkCode(call("DELETE", percentagePath+"?version=3", owner, 22, ""), 409, "INACTIVE_MEMBER_BALANCE")
	var historical []trips.Balance
	decode(check(call("GET", balancesPath, owner, 0, ""), 200), &historical)
	if len(historical) != 2 {
		t.Fatal("former member missing from ledger")
	}
	for _, balance := range historical {
		if balance.Balance != 0 || balance.Active != (balance.UserID == users[0]) {
			t.Fatal("former member ledger changed")
		}
	}
	var newInvite trips.Invitation
	decode(check(call("POST", path+"/invitations", owner, 23, ""), 201), &newInvite)
	check(call("POST", "/trips/join", member, 23, fmt.Sprintf(`{"code":%q}`, newInvite.Code)), 200)
	check(call("DELETE", removePath, owner, 24, ""), 204)
	check(call("DELETE", removePath, owner, 24, ""), 204)
	check(call("GET", settlementsPath, member, 0, ""), 404)
	decode(check(call("GET", settlementsPath, owner, 0, ""), 200), &history)
	if len(history) != 2 {
		t.Fatal("membership changes removed transfer history")
	}
	syncPath := path + "/sync"
	check(call("GET", syncPath, member, 0, ""), 404)
	snapshot := func(wantMembers int) trips.Snapshot {
		t.Helper()
		var snapshot trips.Snapshot
		decode(check(call("GET", syncPath, owner, 0, ""), 200), &snapshot)
		if snapshot.Revision < 1 || snapshot.Trip.ID != trip.ID || len(snapshot.Members) != wantMembers ||
			len(snapshot.Expenses) != 1 || snapshot.Expenses[0].ID != expense.ID || len(snapshot.Balances) != 2 ||
			len(snapshot.Settlements) != 2 || snapshot.Budget.Spent != 12 || snapshot.Budget.Remaining != -12 {
			t.Fatal("unexpected trip snapshot")
		}
		for _, balance := range snapshot.Balances {
			if balance.Balance != 0 {
				t.Fatal("snapshot has a nonzero balance")
			}
		}
		return snapshot
	}
	before := snapshot(1)
	if storage == nil {
		t.Log("receipt integration test skipped because TEST_RECEIPTS_ENDPOINT is unset")
		return
	}
	check(call("POST", "/trips/join", member, 25, fmt.Sprintf(`{"code":%q}`, newInvite.Code)), 200)
	afterJoin := snapshot(2)
	if afterJoin.Revision <= before.Revision {
		t.Fatal("membership change did not advance the snapshot revision")
	}
	var firstSuggestions trips.Suggestions
	decode(check(call("GET", suggestionsPath, owner, 0, ""), 200), &firstSuggestions)
	receiptPath := percentagePath + "/receipt"
	file := func(fill color.Color) []byte {
		t.Helper()
		photo := image.NewRGBA(image.Rect(0, 0, 2, 2))
		photo.Set(0, 0, fill)
		var raw bytes.Buffer
		if err := png.Encode(&raw, photo); err != nil {
			t.Fatal(err)
		}
		return raw.Bytes()
	}
	upload := func(token string, key int, data []byte) result {
		t.Helper()
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "receipt.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/v1"+receiptPath, bytes.NewReader(body.Bytes()))
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Idempotency-Key", fmt.Sprintf("%08x-0000-4000-8000-000000000001", key))
		request.Header.Set("Content-Type", form.FormDataContentType())
		response := httptest.NewRecorder()
		api.ServeHTTP(response, request)
		return result{request, response}
	}
	firstFile := file(color.White)
	secondFile := file(color.RGBA{R: 255, A: 255})
	checkCode(upload(owner, 26, []byte("not an image")), 415, "UNSUPPORTED_RECEIPT_MEDIA")
	checkCode(upload(owner, 26, make([]byte, trips.MaxReceiptSize+1)), 413, "RECEIPT_TOO_LARGE")
	check(upload(outsider, 26, firstFile), 404)
	check(call("GET", receiptPath, member, 0, ""), 404)
	firstMeta := string(check(upload(owner, 26, firstFile), 201))
	if string(check(upload(owner, 26, firstFile), 201)) != firstMeta {
		t.Fatal("different multipart boundary changed the replay")
	}
	checkCode(upload(owner, 26, secondFile), 409, "IDEMPOTENCY_KEY_REUSED")
	check(call("GET", receiptPath, outsider, 0, ""), 404)
	readImage := call("GET", receiptPath, member, 0, "").response
	if readImage.Code != 200 || readImage.Header().Get("Content-Type") != "image/png" ||
		readImage.Header().Get("Cache-Control") != "no-store" || !bytes.Equal(readImage.Body.Bytes(), firstFile) {
		t.Fatalf("incorrect private receipt response: %d", readImage.Code)
	}
	objectKey := func() string {
		t.Helper()
		var key string
		if err := pool.QueryRow(t.Context(), `SELECT object_key FROM receipts WHERE expense_id = $1::text::uuid`, expense.ID).Scan(&key); err != nil {
			t.Fatal(err)
		}
		return key
	}
	firstKey := objectKey()
	anonymous := http.Client{Timeout: 5 * time.Second}
	anonymousResponse, err := anonymous.Get(storageEndpoint + "/" + receiptBucket + "/" + firstKey)
	if err != nil {
		t.Fatal(err)
	}
	_ = anonymousResponse.Body.Close()
	if anonymousResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("object is not private: %d", anonymousResponse.StatusCode)
	}
	afterUpload := snapshot(2)
	if afterUpload.Revision <= afterJoin.Revision || afterUpload.Expenses[0].Receipt == nil || afterUpload.Expenses[0].Receipt.SizeBytes != int64(len(firstFile)) {
		t.Fatal("receipt metadata did not appear in the new snapshot")
	}
	check(upload(member, 27, secondFile), 201)
	secondKey := objectKey()
	if firstKey == secondKey {
		t.Fatal("replacement reused the object key")
	}
	sizes := map[string]int64{firstKey: int64(len(firstFile)), secondKey: int64(len(secondFile))}
	for key, size := range sizes {
		if _, err := storage.Get(t.Context(), key, size); err != nil {
			t.Fatal("object disappeared before it could be queued for cleanup")
		}
	}
	check(call("DELETE", receiptPath, owner, 28, ""), 204)
	check(call("DELETE", receiptPath, owner, 28, ""), 204)
	check(call("GET", receiptPath, owner, 0, ""), 404)
	afterDelete := snapshot(2)
	if afterDelete.Revision <= afterUpload.Revision || afterDelete.Expenses[0].Receipt != nil {
		t.Fatal("snapshot still includes removed receipt")
	}
	var latestSuggestions trips.Suggestions
	decode(check(call("GET", suggestionsPath, owner, 0, ""), 200), &latestSuggestions)
	if latestSuggestions.Revision != firstSuggestions.Revision {
		t.Fatal("attachments should not advance the financial revision")
	}
	if _, err := pool.Exec(t.Context(), `UPDATE receipt_cleanup SET delete_after = clock_timestamp() WHERE object_key = ANY($1::text[])`, []string{firstKey, secondKey}); err != nil {
		t.Fatal(err)
	}
	if err := tripService.CleanupReceipts(t.Context()); err != nil {
		t.Fatal(err)
	}
	for key, size := range sizes {
		if _, err := storage.Get(t.Context(), key, size); err == nil {
			t.Fatal("detached object was not removed")
		}
	}
	var remainingJobs int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM receipt_cleanup WHERE object_key = ANY($1::text[])`, []string{firstKey, secondKey}).Scan(&remainingJobs); err != nil || remainingJobs != 0 {
		t.Fatal("object cleanup did not clear completed records")
	}
}
