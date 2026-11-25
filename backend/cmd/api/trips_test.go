package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Max252t/T-Travels/backend/internal/auth"
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
		for _, table := range []string{"notifications", "invitations", "expense_splits", "expenses", "trip_members"} {
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
	api := newRouter(authService, trips.NewService(pool))
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
}
