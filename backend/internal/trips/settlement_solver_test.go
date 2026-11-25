package trips

import (
	"fmt"
	"slices"
	"testing"
)

func TestExactSettlementCountAgainstBruteForce(t *testing.T) {
	for a := int64(-3); a <= 3; a++ {
		for b := int64(-3); b <= 3; b++ {
			for c := int64(-3); c <= 3; c++ {
				for d := int64(-3); d <= 3; d++ {
					values := []int64{a, b, c, d, -a - b - c - d}
					balances := namedBalances(values)
					got, optimal := suggestTransfers(balances)
					want := bruteCount(slices.Clone(values), 0)
					if !optimal || len(got) != want {
						t.Fatalf("%v: got %d transfers, want %d", values, len(got), want)
					}
					assertTransfersClear(t, balances, got)
				}
			}
		}
	}
}

func TestSettlementThresholdAndStableOrder(t *testing.T) {
	balances := namedBalances([]int64{7, 6, 5, -11, -7, 0})
	transfers, optimal := suggestTransfers(balances)
	if !optimal || len(transfers) != 3 {
		t.Fatalf("expected three optimal transfers, got %v", transfers)
	}
	slices.Reverse(balances)
	reversed, _ := suggestTransfers(balances)
	if !slices.Equal(transfers, reversed) {
		t.Fatal("input order changed the suggestions")
	}
	assertTransfersClear(t, balances, transfers)

	for _, count := range []int{10, 12} {
		values := make([]int64, count)
		for index := range values {
			values[index] = 1
			if index%2 == 0 {
				values[index] = -1
			}
		}
		balances := namedBalances(values)
		transfers, optimal := suggestTransfers(balances)
		if optimal != (count <= exactBalanceLimit) {
			t.Fatalf("incorrect optimal flag for %d balances", count)
		}
		assertTransfersClear(t, balances, transfers)
	}
	if transfers, optimal := suggestTransfers(namedBalances([]int64{1, -2})); transfers != nil || optimal {
		t.Fatal("an unbalanced ledger produced transfer suggestions")
	}
}

func namedBalances(values []int64) []Balance {
	balances := make([]Balance, len(values))
	for i, value := range values {
		balances[i] = Balance{UserID: fmt.Sprintf("%02d", i), Balance: value, Active: true}
	}
	return balances
}

func bruteCount(values []int64, index int) int {
	for index < len(values) && values[index] == 0 {
		index++
	}
	if index == len(values) {
		return 0
	}
	best := len(values)
	for next := index + 1; next < len(values); next++ {
		if (values[index] < 0 && values[next] > 0) || (values[index] > 0 && values[next] < 0) {
			values[next] += values[index]
			best = min(best, 1+bruteCount(values, index+1))
			values[next] -= values[index]
		}
	}
	return best
}

func assertTransfersClear(t *testing.T, balances []Balance, transfers []Transfer) {
	t.Helper()
	remaining := make(map[string]int64, len(balances))
	for _, balance := range balances {
		remaining[balance.UserID] = balance.Balance
	}
	for _, transfer := range transfers {
		if transfer.Amount <= 0 || remaining[transfer.FromUserID] > -transfer.Amount || remaining[transfer.ToUserID] < transfer.Amount {
			t.Fatalf("invalid transfer: %+v", transfer)
		}
		remaining[transfer.FromUserID] += transfer.Amount
		remaining[transfer.ToUserID] -= transfer.Amount
	}
	for user, amount := range remaining {
		if amount != 0 {
			t.Fatalf("user %s has remaining balance %d", user, amount)
		}
	}
}
