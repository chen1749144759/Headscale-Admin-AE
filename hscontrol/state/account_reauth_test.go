package state

import (
	"testing"
	"time"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/util"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func TestPasswordReauthMigratesLegacyAuthKeyNode(t *testing.T) {
	config := newAccountStartupTestConfig(t)
	first, err := NewState(config)
	if err != nil {
		t.Fatalf("creating initial state: %v", err)
	}

	user, err := first.db.CreateUser(types.User{Name: "legacy-network"})
	if err != nil {
		t.Fatalf("creating user: %v", err)
	}
	machineKey := key.NewMachine().Public()
	oldNodeKey := key.NewNode().Public()
	node := types.Node{
		MachineKey:     machineKey,
		NodeKey:        oldNodeKey,
		DiscoKey:       key.NewDisco().Public(),
		Hostname:       "legacy-router",
		GivenName:      "legacy-router",
		UserID:         &user.ID,
		RegisterMethod: util.RegisterMethodAuthKey,
		Hostinfo:       &tailcfg.Hostinfo{Hostname: "legacy-router"},
	}
	if err := first.db.DB.Create(&node).Error; err != nil {
		t.Fatalf("creating legacy node: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("closing initial state: %v", err)
	}

	second, err := NewState(config)
	if err != nil {
		t.Fatalf("reopening state: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	newNodeKey := key.NewNode().Public()
	authID := types.MustAuthID()
	second.SetAuthCacheEntry(authID, types.NewRegisterAuthRequest(&types.RegistrationData{
		MachineKey: machineKey,
		NodeKey:    newNodeKey,
		DiscoKey:   key.NewDisco().Public(),
		Hostname:   "legacy-router",
		Hostinfo:   &tailcfg.Hostinfo{Hostname: "legacy-router"},
	}))
	expiry := time.Now().UTC().Add(24 * time.Hour)
	updated, _, err := second.HandleNodeFromAuthPath(
		authID,
		types.UserID(user.ID),
		&expiry,
		util.RegisterMethodPassword,
	)
	if err != nil {
		t.Fatalf("reauthenticating legacy node: %v", err)
	}
	if got := updated.RegisterMethod(); got != util.RegisterMethodPassword {
		t.Fatalf("updated register method = %q, want %q", got, util.RegisterMethodPassword)
	}
	if got := updated.NodeKey(); got != newNodeKey {
		t.Fatalf("updated node key = %q, want %q", got, newNodeKey)
	}

	var persisted types.Node
	if err := second.db.DB.First(&persisted, node.ID).Error; err != nil {
		t.Fatalf("loading persisted node: %v", err)
	}
	if got := persisted.RegisterMethod; got != util.RegisterMethodPassword {
		t.Fatalf("persisted register method = %q, want %q", got, util.RegisterMethodPassword)
	}
}
