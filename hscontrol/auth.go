package hscontrol

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/rs/zerolog/log"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
)

func (h *Headscale) registrationURL(authID types.AuthID) string {
	return fmt.Sprintf(
		"%s/register/%s",
		strings.TrimSuffix(h.cfg.ServerURL, "/"),
		authID.String(),
	)
}

func (h *Headscale) handleRegister(
	ctx context.Context,
	req tailcfg.RegisterRequest,
	machineKey key.MachinePublic,
	authSource string,
) (*tailcfg.RegisterResponse, error) {
	// Check for logout/expiry FIRST, before checking auth key.
	// Tailscale clients may send logout requests with BOTH a past expiry AND an auth key.
	// A past expiry takes precedence - it's a logout regardless of other fields.
	if !req.Expiry.IsZero() && req.Expiry.Before(time.Now()) {
		log.Debug().
			Str("node.key", req.NodeKey.ShortString()).
			Time("expiry", req.Expiry).
			Bool("has_auth", req.Auth != nil).
			Msg("Detected logout attempt with past expiry")

		// This is a logout attempt (expiry in the past)
		if node, ok := h.state.GetNodeByNodeKey(req.NodeKey); ok {
			log.Debug().
				EmbedObject(node).
				Bool("is_ephemeral", node.IsEphemeral()).
				Bool("has_authkey", node.AuthKey().Valid()).
				Msg("Found existing node for logout, calling handleLogout")

			resp, err := h.handleLogout(node, req, machineKey)
			if err != nil {
				return nil, fmt.Errorf("handling logout: %w", err)
			}

			if resp != nil {
				return resp, nil
			}
		} else {
			log.Warn().
				Str("node.key", req.NodeKey.ShortString()).
				Msg("Logout attempt but node not found in NodeStore")
		}
	}

	// If the register request does not contain a Auth struct, it means we are logging
	// out an existing node (legacy logout path for clients that send Auth=nil).
	if req.Auth == nil {
		// If the register request present a NodeKey that is currently in use, we will
		// check if the node needs to be sent to re-auth, or if the node is logging out.
		// We do not look up nodes by [key.MachinePublic] as it might belong to multiple
		// nodes, separated by users and this path is handling expiring/logout paths.
		if node, ok := h.state.GetNodeByNodeKey(req.NodeKey); ok {
			// Refuse to act on a node looked up purely by NodeKey unless
			// the Noise session's machine key matches the cached node.
			// Without this check anyone holding a target's NodeKey could
			// open a Noise session with a throwaway machine key and read
			// the owner's User/Login back through nodeToRegisterResponse.
			// handleLogout enforces the same check on its own path.
			if node.MachineKey() != machineKey {
				return nil, NewHTTPError(
					http.StatusUnauthorized,
					"node exists with a different machine key",
					nil,
				)
			}

			// When tailscaled restarts, it sends RegisterRequest with Auth=nil and Expiry=zero.
			// Return the current node state without modification.
			if req.Expiry.IsZero() && !node.IsExpired() {
				return nodeToRegisterResponse(node), nil
			}

			resp, err := h.handleLogout(node, req, machineKey)
			if err != nil {
				return nil, fmt.Errorf("handling existing node: %w", err)
			}

			// If resp is not nil, we have a response to return to the node.
			// If resp is nil, we should proceed and see if the node is trying to re-auth.
			if resp != nil {
				return resp, nil
			}
		} else {
			// If the register request is not attempting to register a node, and
			// we cannot match it with an existing node, we consider that unexpected
			// as only register nodes should attempt to log out.
			log.Debug().
				Str("node.key", req.NodeKey.ShortString()).
				Str("machine.key", machineKey.ShortString()).
				Bool("unexpected", true).
				Msg("received register request with no auth, and no existing node")
		}
	}

	// If the [tailcfg.RegisterRequest] has a Followup URL, it means that the
	// node has already started the registration process and we should wait for
	// it to finish the original registration.
	if req.Followup != "" {
		return h.waitForFollowup(ctx, req, machineKey)
	}

	// Pre authenticated keys are handled slightly different than interactive
	// logins as they can be done fully sync and we can respond to the node with
	// the result as it is waiting.
	if isAuthKey(req) {
		return nil, NewHTTPError(
			http.StatusUnauthorized,
			"pre-authentication keys are disabled; use account login",
			nil,
		)
	}

	resp, err := h.handleRegisterInteractive(req, machineKey, authSource)
	if err != nil {
		return nil, fmt.Errorf("handling register interactive: %w", err)
	}

	return resp, nil
}

// handleLogout checks if the [tailcfg.RegisterRequest] is a
// logout attempt from a node. If the node is not attempting to.
func (h *Headscale) handleLogout(
	node types.NodeView,
	req tailcfg.RegisterRequest,
	machineKey key.MachinePublic,
) (*tailcfg.RegisterResponse, error) {
	// Fail closed if it looks like this is an attempt to modify a node where
	// the node key and the machine key the noise session was started with does
	// not align.
	if node.MachineKey() != machineKey {
		return nil, NewHTTPError(http.StatusUnauthorized, "node exist with different machine key", nil)
	}

	// Note: We do NOT return early if req.Auth is set, because Tailscale clients
	// may send logout requests with BOTH a past expiry AND an auth key.
	// A past expiry indicates logout, regardless of whether Auth is present.
	// The expiry check below will handle the logout logic.

	// If the node is expired and this is not a re-authentication attempt,
	// force the client to re-authenticate.
	// TODO(kradalby): I wonder if this is a path we ever hit?
	if node.IsExpired() {
		log.Trace().
			EmbedObject(node).
			Bool("unexpected", true).
			Msg("Node key expired, forcing re-authentication")

		return &tailcfg.RegisterResponse{
			NodeKeyExpired:    true,
			MachineAuthorized: false,
			AuthURL:           "", // Client will need to re-authenticate
		}, nil
	}

	// If we get here, the node is not currently expired, and not trying to
	// do an auth.
	// The node is likely logging out, but before we run that logic, we will validate
	// that the node is not attempting to tamper/extend their expiry.
	// If it is not, we will expire the node or in the case of an ephemeral node, delete it.

	// The client is trying to extend their key, this is not allowed.
	if req.Expiry.After(time.Now()) {
		return nil, NewHTTPError(http.StatusBadRequest, "extending key is not allowed", nil)
	}

	// If the request expiry is in the past, we consider it a logout.
	// Zero expiry is handled in handleRegister() before calling this function.
	if req.Expiry.Before(time.Now()) {
		log.Debug().
			EmbedObject(node).
			Bool("is_ephemeral", node.IsEphemeral()).
			Bool("has_authkey", node.AuthKey().Valid()).
			Time("req.expiry", req.Expiry).
			Msg("Processing logout request with past expiry")

		if node.IsEphemeral() {
			log.Info().
				EmbedObject(node).
				Msg("Deleting ephemeral node during logout")

			c, err := h.state.DeleteNode(node)
			if err != nil {
				return nil, fmt.Errorf("deleting ephemeral node: %w", err)
			}

			h.Change(c)

			return &tailcfg.RegisterResponse{
				NodeKeyExpired:    true,
				MachineAuthorized: false,
			}, nil
		}

		log.Debug().
			EmbedObject(node).
			Msg("Node is not ephemeral, setting expiry instead of deleting")
	}

	// Update the internal state with the nodes new expiry, meaning it is
	// logged out.
	expiry := req.Expiry
	if now := time.Now(); expiry.Before(now) {
		expiry = now
	}

	updatedNode, c, err := h.state.SetNodeExpiry(node.ID(), &expiry)
	if err != nil {
		return nil, fmt.Errorf("setting node expiry: %w", err)
	}

	h.Change(c)

	return nodeToRegisterResponse(updatedNode), nil
}

// isAuthKey reports if the register request is a registration request
// using an pre auth key.
func isAuthKey(req tailcfg.RegisterRequest) bool {
	return req.Auth != nil && req.Auth.AuthKey != ""
}

func nodeToRegisterResponse(node types.NodeView) *tailcfg.RegisterResponse {
	resp := &tailcfg.RegisterResponse{
		NodeKeyExpired: node.IsExpired(),

		// Headscale does not implement the concept of machine authorization
		// so we always return true here.
		// Revisit this if #2176 gets implemented.
		MachineAuthorized: true,
	}

	// For tagged nodes, use the TaggedDevices special user
	// For user-owned nodes, include User and Login information from the actual user
	if node.IsTagged() {
		resp.User = types.TaggedDevices.View().TailscaleUser()
		resp.Login = types.TaggedDevices.View().TailscaleLogin()
	} else if node.Owner().Valid() {
		resp.User = node.Owner().TailscaleUser()
		resp.Login = node.Owner().TailscaleLogin()
	}

	return resp
}

func (h *Headscale) waitForFollowup(
	ctx context.Context,
	req tailcfg.RegisterRequest,
	machineKey key.MachinePublic,
) (*tailcfg.RegisterResponse, error) {
	fu, err := url.Parse(req.Followup)
	if err != nil {
		return nil, NewHTTPError(http.StatusUnauthorized, "invalid followup URL", err)
	}

	followupReg, err := types.AuthIDFromString(strings.ReplaceAll(fu.Path, "/register/", ""))
	if err != nil {
		return nil, NewHTTPError(http.StatusUnauthorized, "invalid registration ID", err)
	}

	if reg, ok := h.state.GetAuthCacheEntry(followupReg); ok {
		regData, isRegistration := reg.RegistrationDataOK()
		if !isRegistration {
			return nil, NewHTTPError(http.StatusUnauthorized, "invalid registration session", nil)
		}
		if regData.MachineKey != machineKey {
			return nil, NewHTTPError(http.StatusUnauthorized, "registration session belongs to another machine", nil)
		}

		handleVerdict := func(verdict types.AuthVerdict) (*tailcfg.RegisterResponse, error) {
			if verdict.Err != nil {
				return nil, NewHTTPError(http.StatusUnauthorized, "registration failed", verdict.Err)
			}
			if !verdict.Node.Valid() {
				return nil, NewHTTPError(http.StatusUnauthorized, "registration did not produce a node", nil)
			}

			return nodeToRegisterResponse(verdict.Node), nil
		}

		if verdict, finished := reg.FinalVerdict(); finished {
			return handleVerdict(verdict)
		}

		select {
		case verdict, ok := <-reg.WaitForAuth():
			if !ok {
				if finalVerdict, finished := reg.FinalVerdict(); finished {
					return handleVerdict(finalVerdict)
				}
				return nil, NewHTTPError(http.StatusUnauthorized, "registration session ended without a result", nil)
			}
			return handleVerdict(verdict)
		case <-ctx.Done():
			// Prefer a verdict that raced with context cancellation. This avoids
			// returning a false timeout after authentication already completed.
			select {
			case verdict, ok := <-reg.WaitForAuth():
				if ok {
					return handleVerdict(verdict)
				}
				if finalVerdict, finished := reg.FinalVerdict(); finished {
					return handleVerdict(finalVerdict)
				}
				return nil, NewHTTPError(http.StatusUnauthorized, "registration session ended without a result", nil)
			default:
				return nil, NewHTTPError(http.StatusUnauthorized, "registration timed out", ctx.Err())
			}
		}
	}

	return nil, NewHTTPError(http.StatusUnauthorized, "registration session has expired", nil)
}

const (
	cachedHostinfoStringLimit = 256
	cachedHostnameStringLimit = 253
	cachedRequestTagLimit     = 16
)

func boundedRegistrationString(value string) string {
	if len(value) <= cachedHostinfoStringLimit {
		return value
	}
	value = value[:cachedHostinfoStringLimit]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}

	return value
}

func normalizedRegistrationHostname(value string) string {
	value = strings.TrimSpace(strings.ToValidUTF8(value, ""))
	var builder strings.Builder
	builder.Grow(min(len(value), cachedHostnameStringLimit))
	lastSeparator := false
	for _, char := range value {
		allowed := unicode.IsLetter(char) || unicode.IsNumber(char) || char == '-' || char == '.' || char == '_'
		if !allowed {
			char = '-'
		}
		separator := char == '-' || char == '.' || char == '_'
		if separator && lastSeparator {
			continue
		}
		builder.WriteRune(char)
		lastSeparator = separator
	}
	hostname := strings.Trim(builder.String(), "-._")
	if len(hostname) > cachedHostnameStringLimit {
		hostname = hostname[:cachedHostnameStringLimit]
		for len(hostname) > 0 && !utf8.ValidString(hostname) {
			hostname = hostname[:len(hostname)-1]
		}
		hostname = strings.Trim(hostname, "-._")
	}
	if hostname == "" {
		return "scaletail-node"
	}

	return hostname
}

func boundedRegistrationStrings(values []string, limit int) []string {
	if len(values) > limit {
		values = values[:limit]
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, boundedRegistrationString(value))
	}

	return result
}

func registrationHostinfo(source *tailcfg.Hostinfo) *tailcfg.Hostinfo {
	if source == nil {
		return nil
	}

	return &tailcfg.Hostinfo{
		IPNVersion:      boundedRegistrationString(source.IPNVersion),
		FrontendLogID:   boundedRegistrationString(source.FrontendLogID),
		BackendLogID:    boundedRegistrationString(source.BackendLogID),
		OS:              boundedRegistrationString(source.OS),
		OSVersion:       boundedRegistrationString(source.OSVersion),
		Container:       source.Container,
		Env:             boundedRegistrationString(source.Env),
		Distro:          boundedRegistrationString(source.Distro),
		DistroVersion:   boundedRegistrationString(source.DistroVersion),
		DistroCodeName:  boundedRegistrationString(source.DistroCodeName),
		App:             boundedRegistrationString(source.App),
		Desktop:         source.Desktop,
		Package:         boundedRegistrationString(source.Package),
		DeviceModel:     boundedRegistrationString(source.DeviceModel),
		Hostname:        normalizedRegistrationHostname(source.Hostname),
		ShieldsUp:       source.ShieldsUp,
		ShareeNode:      source.ShareeNode,
		NoLogsNoSupport: source.NoLogsNoSupport,
		WireIngress:     source.WireIngress,
		IngressEnabled:  source.IngressEnabled,
		AllowsUpdate:    source.AllowsUpdate,
		Machine:         boundedRegistrationString(source.Machine),
		GoArch:          boundedRegistrationString(source.GoArch),
		GoArchVar:       boundedRegistrationString(source.GoArchVar),
		GoVersion:       boundedRegistrationString(source.GoVersion),
		RequestTags:     boundedRegistrationStrings(source.RequestTags, cachedRequestTagLimit),
		Cloud:           boundedRegistrationString(source.Cloud),
		Userspace:       source.Userspace,
		UserspaceRouter: source.UserspaceRouter,
		AppConnector:    source.AppConnector,
		ServicesHash:    boundedRegistrationString(source.ServicesHash),
		PeerRelay:       source.PeerRelay,
		ExitNodeID:      source.ExitNodeID,
		StateEncrypted:  source.StateEncrypted,
	}
}

// registrationDataFromRequest stores a fixed-size metadata projection. Live
// routes, services, endpoints and NetInfo arrive again in the first MapRequest.
func registrationDataFromRequest(
	req tailcfg.RegisterRequest,
	machineKey key.MachinePublic,
) *types.RegistrationData {
	var hostname string
	if req.Hostinfo != nil {
		hostname = normalizedRegistrationHostname(req.Hostinfo.Hostname)
	} else {
		hostname = normalizedRegistrationHostname("")
	}

	regData := &types.RegistrationData{
		MachineKey: machineKey,
		NodeKey:    req.NodeKey,
		Hostname:   hostname,
		Hostinfo:   registrationHostinfo(req.Hostinfo),
	}

	if !req.Expiry.IsZero() {
		expiry := req.Expiry
		regData.Expiry = &expiry
	}

	return regData
}

func (h *Headscale) handleRegisterInteractive(
	req tailcfg.RegisterRequest,
	machineKey key.MachinePublic,
	authSource string,
) (*tailcfg.RegisterResponse, error) {
	if !allowPendingRegistration(authSource, time.Now().UTC()) {
		return nil, NewHTTPError(
			http.StatusTooManyRequests,
			"too many pending registrations from this source",
			nil,
		)
	}
	authID, err := types.NewAuthID()
	if err != nil {
		return nil, fmt.Errorf("generating registration ID: %w", err)
	}

	if req.Hostinfo == nil {
		log.Warn().
			Str("machine.key", machineKey.ShortString()).
			Str("node.key", req.NodeKey.ShortString()).
			Msg("Received registration request with nil hostinfo, generated default hostname")
	} else if req.Hostinfo.Hostname == "" {
		log.Warn().
			Str("machine.key", machineKey.ShortString()).
			Str("node.key", req.NodeKey.ShortString()).
			Msg("Received registration request with empty hostname, generated default")
	}

	authRegReq := types.NewRegisterAuthRequest(
		registrationDataFromRequest(req, machineKey),
	)

	h.state.SetAuthCacheEntry(authID, authRegReq)

	log.Info().Msg("started node registration session")

	return &tailcfg.RegisterResponse{
		AuthURL: h.registrationURL(authID),
	}, nil
}
