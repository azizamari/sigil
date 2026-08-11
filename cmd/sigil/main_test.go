package main

import (
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		wantErr bool
	}{
		{name: "no args prints usage", args: nil, want: "usage: sigil"},
		{name: "help prints usage", args: []string{"help"}, want: "usage: sigil"},
		{name: "version", args: []string{"version"}, want: "dev"},
		{name: "unknown command errors", args: []string{"nope"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out strings.Builder
			err := run(tt.args, &out)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("run(%q) = nil error, want error", tt.args)
				}
				return
			}
			if err != nil {
				t.Fatalf("run(%q) = %v, want nil error", tt.args, err)
			}
			if !strings.Contains(out.String(), tt.want) {
				t.Errorf("run(%q) output = %q, want it to contain %q", tt.args, out.String(), tt.want)
			}
		})
	}
}
