package trips

import (
	"math/bits"
	"slices"
	"strings"
)

const exactBalanceLimit = 10

type Transfer struct {
	FromUserID string `json:"from_user_id"`
	ToUserID   string `json:"to_user_id"`
	Amount     int64  `json:"amount"`
}

func suggestTransfers(balances []Balance) ([]Transfer, bool) {
	nonzero := make([]Balance, 0, len(balances))
	for _, balance := range balances {
		if balance.Balance != 0 {
			nonzero = append(nonzero, balance)
		}
	}
	slices.SortFunc(nonzero, func(a, b Balance) int { return strings.Compare(a.UserID, b.UserID) })
	transfers := make([]Transfer, 0, len(nonzero))
	if len(nonzero) > exactBalanceLimit {
		return settleGroup(transfers, nonzero), false
	}

	limit := 1 << len(nonzero)
	sums := make([]int64, limit)
	for set := 1; set < limit; set++ {
		first := bits.TrailingZeros(uint(set))
		sums[set] = nonzero[first].Balance + sums[set&(set-1)]
	}
	groupCount := make([]int, limit)
	firstGroup := make([]int, limit)
	for set := 1; set < limit; set++ {
		if sums[set] != 0 {
			continue
		}
		anchor := set & -set
		for group := set; group > 0; group = (group - 1) & set {
			if group&anchor != 0 && sums[group] == 0 && groupCount[set^group]+1 > groupCount[set] {
				groupCount[set] = groupCount[set^group] + 1
				firstGroup[set] = group
			}
		}
	}
	for remaining := limit - 1; remaining != 0; remaining ^= firstGroup[remaining] {
		if firstGroup[remaining] == 0 {
			return nil, false
		}
		group := make([]Balance, 0, len(nonzero))
		for set := firstGroup[remaining]; set > 0; set &= set - 1 {
			group = append(group, nonzero[bits.TrailingZeros(uint(set))])
		}
		transfers = settleGroup(transfers, group)
	}
	return transfers, true
}

func settleGroup(transfers []Transfer, group []Balance) []Transfer {
	debtors, creditors := make([]Balance, 0, len(group)), make([]Balance, 0, len(group))
	for _, balance := range group {
		if balance.Balance < 0 {
			debtors = append(debtors, balance)
		} else {
			creditors = append(creditors, balance)
		}
	}
	debtor, creditor := 0, 0
	for debtor < len(debtors) && creditor < len(creditors) {
		from, to := &debtors[debtor], &creditors[creditor]
		amount := min(-from.Balance, to.Balance)
		transfers = append(transfers, Transfer{FromUserID: from.UserID, ToUserID: to.UserID, Amount: amount})
		from.Balance += amount
		to.Balance -= amount
		if from.Balance == 0 {
			debtor++
		}
		if to.Balance == 0 {
			creditor++
		}
	}
	if debtor != len(debtors) || creditor != len(creditors) {
		return nil
	}
	return transfers
}
