package cmd

import (
	"reflect"
	"testing"
)

func TestUpdateBuildRemoteFlag(t *testing.T) {
	tests := []struct {
		name string
		tgt  updateBuildTarget
		want []chosenFlag
	}{
		{"local bakes nothing", updateBuildTarget{}, nil},
		{"named bakes --from=<name>", updateBuildTarget{remote: true, fromName: "prod"},
			[]chosenFlag{{name: "--from", value: "prod", sep: "="}}},
		{"linked bakes --remote", updateBuildTarget{remote: true, linked: true},
			[]chosenFlag{{name: "--remote"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateBuildRemoteFlag(tc.tgt); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("updateBuildRemoteFlag = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestUpdateBuildComposedLine asserts the full composed line for each target
// mode given a fixed module pick and one extra flag — the "bake correctness"
// the builder guarantees, verifiable without a live remote.
func TestUpdateBuildComposedLine(t *testing.T) {
	picked := []string{"base", "web"}
	extras := []chosenFlag{{name: "--i18n"}}

	cases := []struct {
		name string
		tgt  updateBuildTarget
		want []string
	}{
		{"local", updateBuildTarget{}, []string{"base", "web", "--i18n"}},
		{"named", updateBuildTarget{remote: true, fromName: "prod"},
			[]string{"base", "web", "--from=prod", "--i18n"}},
		{"linked", updateBuildTarget{remote: true, linked: true},
			[]string{"base", "web", "--remote", "--i18n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flags := append(updateBuildRemoteFlag(tc.tgt), extras...)
			got := composeArgs(picked, flags)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("composed = %#v, want %#v", got, tc.want)
			}
		})
	}
}
