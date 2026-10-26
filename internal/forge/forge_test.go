package forge_test

import (
	"slices"
	"testing"

	"github.com/UnPoilTefal/night-shift/internal/forge"
)

func TestBlockedByReadsEveryBlockedByLineOfTheBody(t *testing.T) {
	for body, want := range map[string][]int{
		"Le brief.\n\nBlocked by: #12":                 {12},
		"Blocked by: #3, #4\nsuite\nblocked by: #4,#5": {3, 4, 5},
		"  Blocked by:   #7  \r\n":                     {7},
		"Le brief, blocked by: #9 en passant.":         nil,
		"Blocked by: #9 et la suite":                   nil,
		"Blocked by: 9":                                nil,
		"## Blocked by\n\n- #80":                       nil,
		"":                                             nil,
	} {
		if got := forge.BlockedBy(body); !slices.Equal(got, want) {
			t.Errorf("BlockedBy(%q) = %v, attendu %v", body, got, want)
		}
	}
}
