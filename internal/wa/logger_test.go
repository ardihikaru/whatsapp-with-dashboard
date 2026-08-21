package wa

import "testing"

func TestRecoverableSignalFanoutWarningClassifier(t *testing.T) {
	tests := []struct {
		name string
		msg  string
		want bool
	}{
		{
			name: "partial encryption fanout failure",
			msg:  "Failed to encrypt %s for %s: %v",
			want: true,
		},
		{
			name: "identity refresh retry",
			msg:  "Got %v error while trying to process prekey bundle for %s, clearing stored identity and retrying",
			want: true,
		},
		{
			name: "keepalive warning remains visible",
			msg:  "Keepalive timed out",
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRecoverableSignalFanoutWarning(tt.msg); got != tt.want {
				t.Fatalf("isRecoverableSignalFanoutWarning() = %v, want %v", got, tt.want)
			}
		})
	}
}
