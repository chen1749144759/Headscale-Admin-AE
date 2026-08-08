package hscontrol

import (
	"context"
	"testing"

	v1 "github.com/juanfont/headscale/gen/go/headscale/v1"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestAPIKeyManagementDisabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	api := headscaleV1APIServer{}

	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "create",
			call: func() error {
				_, err := api.CreateApiKey(ctx, &v1.CreateApiKeyRequest{})
				return err
			},
		},
		{
			name: "expire",
			call: func() error {
				_, err := api.ExpireApiKey(ctx, &v1.ExpireApiKeyRequest{})
				return err
			},
		},
		{
			name: "list",
			call: func() error {
				_, err := api.ListApiKeys(ctx, &v1.ListApiKeysRequest{})
				return err
			},
		},
		{
			name: "delete",
			call: func() error {
				_, err := api.DeleteApiKey(ctx, &v1.DeleteApiKeyRequest{})
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.ErrorContains(t, err, "API key management is disabled")
		})
	}
}

func TestManualNodeAuthenticationDisabled(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	api := headscaleV1APIServer{}
	tests := []struct {
		name string
		call func() error
	}{
		{"register-node", func() error {
			_, err := api.RegisterNode(ctx, &v1.RegisterNodeRequest{})
			return err
		}},
		{"auth-register", func() error {
			_, err := api.AuthRegister(ctx, &v1.AuthRegisterRequest{})
			return err
		}},
		{"auth-approve", func() error {
			_, err := api.AuthApprove(ctx, &v1.AuthApproveRequest{})
			return err
		}},
		{"auth-reject", func() error {
			_, err := api.AuthReject(ctx, &v1.AuthRejectRequest{})
			return err
		}},
		{"debug-create-node", func() error {
			_, err := api.DebugCreateNode(ctx, &v1.DebugCreateNodeRequest{})
			return err
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.call()
			require.Equal(t, codes.FailedPrecondition, status.Code(err))
			require.ErrorContains(t, err, "account login")
		})
	}
}
