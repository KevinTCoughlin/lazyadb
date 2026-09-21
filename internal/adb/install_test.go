package adb

import (
	"errors"
	"strings"
	"testing"
)

// adb has two failure conventions across versions: older builds exit 0 while
// printing "Failure [...]", newer ones exit non-zero. Treating the first as
// success would make a fleet install lie about which devices took the build.
func TestInstallError(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		err     error
		wantErr bool
		want    string
	}{
		{
			name:   "success",
			output: "Performing Streamed Install\nSuccess",
		},
		{
			name:    "failure printed but exit 0",
			output:  "Failure [INSTALL_FAILED_ALREADY_EXISTS]",
			err:     nil,
			wantErr: true,
			want:    "INSTALL_FAILED_ALREADY_EXISTS",
		},
		{
			name:    "adb failed to install",
			output:  "adb: failed to install app.apk: Failure [INSTALL_FAILED_INSUFFICIENT_STORAGE]",
			err:     errors.New("exit status 1"),
			wantErr: true,
			want:    "INSTALL_FAILED_INSUFFICIENT_STORAGE",
		},
		{
			name:    "error line",
			output:  "Error: device offline",
			err:     errors.New("exit status 1"),
			wantErr: true,
			want:    "device offline",
		},
		{
			name:    "non-zero exit with unrecognized output",
			output:  "something went sideways",
			err:     errors.New("exit status 1"),
			wantErr: true,
			want:    "something went sideways",
		},
		{
			name:    "non-zero exit with no output",
			err:     errors.New("exit status 1"),
			wantErr: true,
			want:    "exit status 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := installError(tt.output, tt.err)
			if tt.wantErr != (err != nil) {
				t.Fatalf("installError() = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), tt.want) {
				t.Errorf("installError() = %q, want it to mention %q", err, tt.want)
			}
		})
	}
}
