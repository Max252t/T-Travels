package trips

import (
	"cmp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
)

const (
	maxExpenseParticipants = 100
	percentageTotal        = int64(10_000)
)

type ParticipantInput struct {
	UserID      string `json:"user_id"`
	Amount      *int64 `json:"amount,omitempty"`
	BasisPoints *int64 `json:"basis_points,omitempty"`
}

type Share struct {
	UserID      string `json:"user_id"`
	Amount      int64  `json:"amount"`
	BasisPoints *int64 `json:"basis_points,omitempty"`
}

func canonicalParticipants(input []ParticipantInput) ([]ParticipantInput, error) {
	if len(input) == 0 || len(input) > maxExpenseParticipants {
		return nil, ErrInvalidExpense
	}
	participants := slices.Clone(input)
	for i := range participants {
		id, ok := expenseUUID(participants[i].UserID)
		if !ok {
			return nil, ErrInvalidExpense
		}
		participants[i].UserID = id
	}
	slices.SortFunc(participants, func(a, b ParticipantInput) int { return strings.Compare(a.UserID, b.UserID) })
	for i := 1; i < len(participants); i++ {
		if participants[i-1].UserID == participants[i].UserID {
			return nil, ErrInvalidExpense
		}
	}
	return participants, nil
}

func expenseUUID(raw string) (string, bool) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil || !strings.EqualFold(raw, id.String()) {
		return "", false
	}
	return id.String(), true
}

func calculateShares(amount int64, splitType string, participants []ParticipantInput) ([]Share, error) {
	if amount < 1 || amount > MaxBudget || len(participants) == 0 || len(participants) > maxExpenseParticipants {
		return nil, ErrInvalidExpense
	}
	shares := make([]Share, len(participants))
	for i, participant := range participants {
		shares[i].UserID = participant.UserID
	}
	switch splitType {
	case "equal":
		count := int64(len(participants))
		for i, participant := range participants {
			if participant.Amount != nil || participant.BasisPoints != nil {
				return nil, ErrInvalidExpense
			}
			shares[i].Amount = amount / count
			if int64(i) < amount%count {
				shares[i].Amount++
			}
		}
	case "exact":
		remaining := amount
		for i, participant := range participants {
			if participant.Amount == nil || *participant.Amount < 0 || *participant.Amount > remaining || participant.BasisPoints != nil {
				return nil, ErrInvalidExpense
			}
			shares[i].Amount = *participant.Amount
			remaining -= shares[i].Amount
		}
		if remaining != 0 {
			return nil, ErrInvalidExpense
		}
	case "percentage":
		type fraction struct {
			index     int
			remainder int64
		}
		fractions := make([]fraction, len(participants))
		pointsLeft, penniesLeft := percentageTotal, amount
		for i, participant := range participants {
			if participant.BasisPoints == nil || *participant.BasisPoints < 0 || *participant.BasisPoints > pointsLeft || participant.Amount != nil {
				return nil, ErrInvalidExpense
			}
			points := *participant.BasisPoints
			product := amount * points
			shares[i].Amount = product / percentageTotal
			shares[i].BasisPoints = new(points)
			fractions[i] = fraction{i, product % percentageTotal}
			pointsLeft -= points
			penniesLeft -= shares[i].Amount
		}
		if pointsLeft != 0 {
			return nil, ErrInvalidExpense
		}
		slices.SortStableFunc(fractions, func(a, b fraction) int { return cmp.Compare(b.remainder, a.remainder) })
		for _, fraction := range fractions[:penniesLeft] {
			shares[fraction.index].Amount++
		}
	default:
		return nil, ErrInvalidExpense
	}
	return shares, nil
}

func savedSplitInput(expense Expense) []ParticipantInput {
	participants := make([]ParticipantInput, len(expense.Participants))
	for i, share := range expense.Participants {
		participants[i] = ParticipantInput{UserID: share.UserID, BasisPoints: share.BasisPoints}
		if expense.SplitType == "exact" {
			participants[i].Amount = new(share.Amount)
		}
	}
	return participants
}
