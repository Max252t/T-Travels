package trips

import (
	"slices"
	"testing"
)

func TestCalculateShares(t *testing.T) {
	const first = "11111111-1111-4111-8111-111111111111"
	const second = "22222222-2222-4222-8222-222222222222"
	const third = "33333333-3333-4333-8333-333333333333"
	for _, test := range []struct {
		name         string
		amount       int64
		mode         string
		participants []ParticipantInput
		want         []int64
	}{
		{"equal rounding", 100, "equal", []ParticipantInput{{UserID: third}, {UserID: first}, {UserID: second}}, []int64{34, 33, 33}},
		{"percentage rounding", 5, "percentage", []ParticipantInput{{UserID: third, BasisPoints: new(int64(3334))},
			{UserID: second, BasisPoints: new(int64(3333))}, {UserID: first, BasisPoints: new(int64(3333))}}, []int64{2, 1, 2}},
		{"exact with zero", 33, "exact", []ParticipantInput{{UserID: first, Amount: new(int64(0))}, {UserID: second, Amount: new(int64(33))}}, []int64{0, 33}},
		{"maximum", MaxBudget, "percentage", []ParticipantInput{{UserID: first, BasisPoints: new(int64(10_000))}}, []int64{MaxBudget}},
		{"duplicate", 2, "equal", []ParticipantInput{{UserID: first}, {UserID: first}}, nil},
		{"extra amount", 2, "equal", []ParticipantInput{{UserID: first, Amount: new(int64(2))}}, nil},
		{"negative exact share", 2, "exact", []ParticipantInput{{UserID: first, Amount: new(int64(-1))}}, nil},
		{"exact sum mismatch", 2, "exact", []ParticipantInput{{UserID: first, Amount: new(int64(1))}}, nil},
		{"percentage sum mismatch", 1, "percentage", []ParticipantInput{{UserID: first, BasisPoints: new(int64(9999))}}, nil},
		{"above maximum", MaxBudget + 1, "equal", []ParticipantInput{{UserID: first}}, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			participants, err := canonicalParticipants(test.participants)
			var shares []Share
			if err == nil {
				shares, err = calculateShares(test.amount, test.mode, participants)
			}
			if test.want == nil {
				if err == nil {
					t.Fatal("invalid split accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var amounts []int64
			for _, share := range shares {
				amounts = append(amounts, share.Amount)
			}
			if !slices.Equal(amounts, test.want) {
				t.Fatalf("got %v, want %v", amounts, test.want)
			}
		})
	}
}
