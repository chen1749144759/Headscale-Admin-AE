package types

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestAuthRequestRegistrationClaimIsAtomic(t *testing.T) {
	authRequest := NewRegisterAuthRequest(&RegistrationData{})

	var claims atomic.Int32
	var waitGroup sync.WaitGroup
	for range 32 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if authRequest.TryClaimRegistration() {
				claims.Add(1)
			}
		}()
	}
	waitGroup.Wait()

	if got := claims.Load(); got != 1 {
		t.Fatalf("successful claims = %d, want 1", got)
	}
}

func TestAuthRequestRegistrationDataOK(t *testing.T) {
	if _, ok := new(AuthRequest).RegistrationDataOK(); ok {
		t.Fatal("malformed auth request reported registration data")
	}
}
