package utils

import (
	"testing"

	"k8s.io/apimachinery/pkg/version"
)

func Test_setOutOfTaintSupportedFlag(t *testing.T) {
	type args struct {
		version *version.Info
	}
	tests := []struct {
		name                    string
		args                    args
		wantErr                 bool
		isOutOfTaintFlagEnabled bool
	}{
		//valid use-cases
		{name: "validEnabledNoPlus", args: args{&version.Info{Major: "1", Minor: "26"}}, wantErr: false, isOutOfTaintFlagEnabled: true},
		{name: "validDisabledEnabledNoPlus", args: args{&version.Info{Major: "1", Minor: "24"}}, wantErr: false, isOutOfTaintFlagEnabled: false},
		{name: "validEnabledWithPlus", args: args{&version.Info{Major: "1", Minor: "26+"}}, wantErr: false, isOutOfTaintFlagEnabled: true},
		{name: "validDisabledWithPlus", args: args{&version.Info{Major: "1", Minor: "24+"}}, wantErr: false, isOutOfTaintFlagEnabled: false},
		{name: "validEnabledWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "26.5.2#$%+"}}, wantErr: false, isOutOfTaintFlagEnabled: true},
		{name: "validDisabledWithTrailingChars", args: args{&version.Info{Major: "1", Minor: "22.5.2#$%+"}}, wantErr: false, isOutOfTaintFlagEnabled: false},

		//invalid use-cases
		{name: "inValidNoPlus", args: args{&version.Info{Major: "1", Minor: "%24"}}, wantErr: true, isOutOfTaintFlagEnabled: false},
		{name: "inValidWithPlus", args: args{&version.Info{Major: "1+", Minor: "26"}}, wantErr: true, isOutOfTaintFlagEnabled: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			IsOutOfServiceTaintSupported = false
			if err := setOutOfTaintSupportedFlag(tt.args.version); (err != nil) != tt.wantErr || IsOutOfServiceTaintSupported != tt.isOutOfTaintFlagEnabled {
				t.Errorf("setOutOfTaintSupportedFlag() error = %v, wantErr %v, expected out of taint flag %v", err, tt.wantErr, tt.isOutOfTaintFlagEnabled)
			}
		})
	}
}
