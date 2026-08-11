package hscontrol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	hsdb "github.com/juanfont/headscale/hscontrol/db"
	"github.com/juanfont/headscale/hscontrol/state"
	"github.com/juanfont/headscale/hscontrol/types"
	"github.com/juanfont/headscale/hscontrol/types/change"
	"github.com/rs/zerolog/log"
	"gorm.io/gorm"
	"tailscale.com/util/dnsname"
)

const (
	scaleForgeAPIBodyLimit int64 = 64 << 10
	scaleForgeAuthPrefix         = "Bearer "
)

type scaleForgeAccountResponse struct {
	ID                 uint       `json:"id"`
	Username           string     `json:"username"`
	Role               string     `json:"role"`
	Enabled            bool       `json:"enabled"`
	ExpiresAt          *time.Time `json:"expiresAt,omitempty"`
	PasswordChangedAt  time.Time  `json:"passwordChangedAt"`
	MustChangePassword bool       `json:"mustChangePassword"`
	LastLoginAt        *time.Time `json:"lastLoginAt,omitempty"`
	UserID             *uint      `json:"userId,omitempty"`
	NetworkName        string     `json:"networkName,omitempty"`
	GroupID            *uint      `json:"groupId,omitempty"`
	GroupName          string     `json:"groupName,omitempty"`
}

func scaleForgeAccountDTO(account *types.Account) scaleForgeAccountResponse {
	response := scaleForgeAccountResponse{
		ID:                 account.ID,
		Username:           account.Username,
		Role:               account.Role,
		Enabled:            account.Enabled,
		ExpiresAt:          account.ExpiresAt,
		PasswordChangedAt:  account.PasswordChangedAt,
		MustChangePassword: account.MustChangePassword,
		LastLoginAt:        account.LastLoginAt,
		UserID:             account.UserID,
		GroupID:            account.GroupID,
	}
	if account.User != nil {
		response.NetworkName = account.User.Name
	}
	if account.Group != nil {
		response.GroupName = account.Group.Name
	}

	return response
}

func bootstrapScaleForgeAccount(cfg *types.Config, appState *state.State) error {
	count, err := appState.CountAccounts()
	if err != nil {
		return err
	}
	if count > 0 {
		if err := appState.ValidateManagerAccountInvariant(); err == nil {
			return nil
		} else if !errors.Is(err, hsdb.ErrLastManager) {
			return fmt.Errorf("validating manager account invariant: %w", err)
		}
	}

	username := types.NormalizeAccountUsername(cfg.ScaleForge.BootstrapUsername)
	passwordFile := strings.TrimSpace(cfg.ScaleForge.BootstrapPasswordFile)
	if username == "" || passwordFile == "" {
		if count > 0 {
			return fmt.Errorf("%w: configure scaleforge.bootstrap_username and scaleforge.bootstrap_password_file", hsdb.ErrLastManager)
		}
		return errors.New("no account exists; configure scaleforge.bootstrap_username and scaleforge.bootstrap_password_file")
	}

	passwordBytes, err := os.ReadFile(os.ExpandEnv(passwordFile))
	if err != nil {
		return fmt.Errorf("reading bootstrap password file: %w", err)
	}
	password := strings.TrimRight(string(passwordBytes), "\r\n")
	if _, err := appState.BootstrapManagerAccount(username, password); err != nil {
		return err
	}
	if err := appState.ValidateManagerAccountInvariant(); err != nil {
		return fmt.Errorf("validating recovered manager account: %w", err)
	}

	if count == 0 {
		log.Info().Msg("created initial ScaleForge manager account")
	} else {
		log.Warn().Msg("recovered ScaleForge manager account from bootstrap credentials")
	}
	return nil
}

type scaleForgeAPI struct {
	headscale *Headscale
}

func (h *Headscale) newScaleForgeAPIServer(headscaleAPI http.Handler) *http.Server {
	api := &scaleForgeAPI{headscale: h}
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/v1/health" {
				if err := verifyScaleForgeInternalRequest(
					req,
					h.scaleForgeAuthKey,
					h.scaleForgeReplay,
					time.Now().UTC(),
				); err != nil {
					writeScaleForgeError(writer, http.StatusUnauthorized, "internal_auth_failed", "internal request authentication failed")
					return
				}
			}
			req.Body = http.MaxBytesReader(writer, req.Body, scaleForgeAPIBodyLimit)
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("X-Content-Type-Options", "nosniff")
			next.ServeHTTP(writer, req)
		})
	})

	router.Get("/v1/health", api.health)
	router.Handle("/api/v1/*", api.requireGatewaySession(headscaleAPI))
	router.Post("/v1/sessions", api.login)
	router.Get("/v1/session", api.session)
	router.Delete("/v1/session", api.logout)
	router.Put("/v1/session/password", api.changeOwnPassword)
	router.Get("/v1/accounts", api.listAccounts)
	router.Post("/v1/accounts", api.createAccount)
	router.Patch("/v1/accounts/{accountID}", api.updateAccount)
	router.Put("/v1/accounts/{accountID}/password", api.resetAccountPassword)
	router.Get("/v1/groups", api.listAccountGroups)
	router.Post("/v1/groups", api.createAccountGroup)
	router.Delete("/v1/groups/{groupID}", api.deleteAccountGroup)
	router.Get("/v1/dns", api.getDNS)
	router.Put("/v1/dns", api.updateDNS)

	return &http.Server{
		Handler:           router,
		ReadHeaderTimeout: types.HTTPTimeout,
		ReadTimeout:       types.HTTPTimeout,
		WriteTimeout:      types.HTTPTimeout,
	}
}

func writeScaleForgeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if value != nil {
		if err := json.NewEncoder(writer).Encode(value); err != nil {
			log.Error().Err(err).Msg("encoding ScaleForge API response")
		}
	}
}

func writeScaleForgeError(writer http.ResponseWriter, status int, code, message string) {
	writeScaleForgeJSON(writer, status, map[string]string{"code": code, "error": message})
}

func writeScaleForgeAccountMutationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hsdb.ErrAccountUsernameExists):
		writeScaleForgeError(writer, http.StatusConflict, "username_exists", "username already exists")
	case errors.Is(err, hsdb.ErrAccountHasNoGroup):
		writeScaleForgeError(writer, http.StatusBadRequest, "group_required", "user account requires a group")
	case errors.Is(err, hsdb.ErrAccountGroupNotFound):
		writeScaleForgeError(writer, http.StatusNotFound, "group_not_found", "group not found")
	case errors.Is(err, hsdb.ErrLastManager):
		writeScaleForgeError(writer, http.StatusConflict, "last_manager", "at least one enabled manager is required")
	case errors.Is(err, hsdb.ErrAccountConcurrentUpdate):
		writeScaleForgeError(writer, http.StatusConflict, "account_changed", "account changed; reload and retry")
	default:
		log.Error().Err(err).Msg("mutating ScaleForge account")
		writeScaleForgeError(writer, http.StatusBadRequest, "invalid_account", "account data is invalid")
	}
}

func decodeScaleForgeJSON(writer http.ResponseWriter, req *http.Request, value any) bool {
	decoder := json.NewDecoder(req.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		writeScaleForgeError(writer, http.StatusBadRequest, "invalid_request", "invalid request body")
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeScaleForgeError(writer, http.StatusBadRequest, "invalid_request", "request body must contain one JSON value")
		return false
	}

	return true
}

func scaleForgeBearerToken(req *http.Request) string {
	authorization := strings.TrimSpace(req.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, scaleForgeAuthPrefix) {
		return ""
	}

	return strings.TrimSpace(strings.TrimPrefix(authorization, scaleForgeAuthPrefix))
}

func (api *scaleForgeAPI) authenticatedSession(
	writer http.ResponseWriter,
	req *http.Request,
	managerRequired,
	allowRestricted bool,
) (*types.AccountSession, string, bool) {
	token := scaleForgeBearerToken(req)
	if token == "" {
		writeScaleForgeError(writer, http.StatusUnauthorized, "session_required", "authentication required")
		return nil, "", false
	}

	session, err := api.headscale.state.ValidateAccountSession(token, time.Now().UTC())
	if err != nil {
		writeScaleForgeError(writer, http.StatusUnauthorized, "invalid_session", "session is invalid or expired")
		return nil, "", false
	}
	if session.Restricted && !allowRestricted {
		writeScaleForgeError(writer, http.StatusForbidden, "password_change_required", "password must be changed")
		return nil, "", false
	}
	if managerRequired && session.Account.Role != types.AccountRoleManager {
		writeScaleForgeError(writer, http.StatusForbidden, "manager_required", "manager permission required")
		return nil, "", false
	}

	return session, token, true
}

func (api *scaleForgeAPI) requireGatewaySession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		session, _, ok := api.authenticatedSession(writer, req, false, false)
		if !ok {
			return
		}
		if session.Account.Role == types.AccountRoleManager {
			next.ServeHTTP(writer, req)
			return
		}

		if !api.authorizeAccountGatewayRequest(&session.Account, req) {
			writeScaleForgeError(writer, http.StatusForbidden, "gateway_access_denied", "account access is limited to its own nodes")
			return
		}

		next.ServeHTTP(writer, req)
	})
}

func (api *scaleForgeAPI) authorizeAccountGatewayRequest(account *types.Account, req *http.Request) bool {
	if account.UserID == nil || account.User == nil || account.User.Name == "" {
		return false
	}

	path := strings.Split(strings.Trim(req.URL.Path, "/"), "/")
	// A regular account may list only the nodes in its bound Headscale network.
	if len(path) == 3 && path[0] == "api" && path[1] == "v1" && path[2] == "node" {
		if req.Method != http.MethodGet {
			return false
		}
		query := req.URL.Query()
		query.Set("user", account.User.Name)
		req.URL.RawQuery = query.Encode()
		return true
	}

	if len(path) < 4 || path[0] != "api" || path[1] != "v1" || path[2] != "node" {
		return false
	}
	nodeID, err := strconv.ParseUint(path[3], 10, 64)
	if err != nil || nodeID == 0 {
		return false
	}
	node, ok := api.headscale.state.GetNodeByID(types.NodeID(nodeID))
	if !ok || node.TypedUserID() != types.UserID(*account.UserID) {
		return false
	}

	switch {
	case len(path) == 4 && req.Method == http.MethodGet:
		return true
	case len(path) == 5 && path[4] == "routes" && req.Method == http.MethodGet:
		return true
	default:
		return false
	}
}

func (api *scaleForgeAPI) health(writer http.ResponseWriter, _ *http.Request) {
	writeScaleForgeJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

func scaleForgeAuthenticationSource(req *http.Request) string {
	value := strings.TrimSpace(req.Header.Get("X-ScaleForge-Source"))
	address, err := netip.ParseAddr(value)
	if err != nil {
		return "unknown"
	}

	return address.Unmap().String()
}

func (api *scaleForgeAPI) login(writer http.ResponseWriter, req *http.Request) {
	var loginRequest struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeScaleForgeJSON(writer, req, &loginRequest) {
		return
	}

	now := time.Now().UTC()
	account, err := api.headscale.authenticateScaleForgeAccount(
		req.Context(),
		loginRequest.Username,
		loginRequest.Password,
		scaleForgeAuthenticationSource(req),
		now,
	)
	restricted := errors.Is(err, hsdb.ErrAccountPasswordExpired) && account != nil
	if err != nil && !restricted {
		status, code, message := http.StatusUnauthorized, "invalid_credentials", "invalid username or password"
		switch {
		case errors.Is(err, errPasswordAuthRateLimited):
			status, code, message = http.StatusTooManyRequests, "too_many_attempts", "too many authentication attempts"
		case errors.Is(err, hsdb.ErrAccountDisabled):
			status, code, message = http.StatusForbidden, "account_disabled", "account is disabled"
		case errors.Is(err, hsdb.ErrAccountExpired):
			status, code, message = http.StatusForbidden, "account_expired", "account is expired"
		case !errors.Is(err, hsdb.ErrAccountInvalidCredentials):
			log.Error().Err(err).Msg("ScaleForge account login failed")
			status, code, message = http.StatusInternalServerError, "internal_error", "login failed"
		}
		writeScaleForgeError(writer, status, code, message)
		return
	}

	token, session, err := api.headscale.state.CreateAccountSession(account, restricted, now)
	if err != nil {
		log.Error().Err(err).Msg("creating ScaleForge account session")
		writeScaleForgeError(writer, http.StatusInternalServerError, "internal_error", "login failed")
		return
	}

	writeScaleForgeJSON(writer, http.StatusOK, map[string]any{
		"token":              token,
		"expiresAt":          session.ExpiresAt,
		"mustChangePassword": restricted,
		"account":            scaleForgeAccountDTO(account),
	})
}

func (api *scaleForgeAPI) session(writer http.ResponseWriter, req *http.Request) {
	session, _, ok := api.authenticatedSession(writer, req, false, true)
	if !ok {
		return
	}

	writeScaleForgeJSON(writer, http.StatusOK, map[string]any{
		"expiresAt":          session.ExpiresAt,
		"mustChangePassword": session.Restricted,
		"account":            scaleForgeAccountDTO(&session.Account),
	})
}

func (api *scaleForgeAPI) logout(writer http.ResponseWriter, req *http.Request) {
	_, token, ok := api.authenticatedSession(writer, req, false, true)
	if !ok {
		return
	}
	if err := api.headscale.state.RevokeAccountSession(token, time.Now().UTC()); err != nil {
		log.Error().Err(err).Msg("revoking ScaleForge account session")
		writeScaleForgeError(writer, http.StatusInternalServerError, "internal_error", "logout failed")
		return
	}

	writer.WriteHeader(http.StatusNoContent)
}

func (api *scaleForgeAPI) changeOwnPassword(writer http.ResponseWriter, req *http.Request) {
	session, _, ok := api.authenticatedSession(writer, req, false, true)
	if !ok {
		return
	}
	var passwordRequest struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeScaleForgeJSON(writer, req, &passwordRequest) {
		return
	}

	now := time.Now().UTC()
	account, err := api.headscale.authenticateScaleForgeAccount(
		req.Context(),
		session.Account.Username,
		passwordRequest.CurrentPassword,
		scaleForgeAuthenticationSource(req),
		now,
	)
	if errors.Is(err, errPasswordAuthRateLimited) {
		writeScaleForgeError(writer, http.StatusTooManyRequests, "too_many_attempts", "too many authentication attempts")
		return
	}
	if err != nil && !errors.Is(err, hsdb.ErrAccountPasswordExpired) {
		writeScaleForgeError(writer, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	if account == nil || account.ID != session.AccountID {
		writeScaleForgeError(writer, http.StatusUnauthorized, "invalid_credentials", "current password is incorrect")
		return
	}
	if passwordRequest.CurrentPassword == passwordRequest.NewPassword {
		writeScaleForgeError(writer, http.StatusBadRequest, "password_reused", "new password must be different")
		return
	}
	changes, err := api.headscale.state.ChangeAccountPassword(account.ID, passwordRequest.NewPassword, now)
	api.headscale.Change(changes...)
	if err != nil {
		status, code := http.StatusBadRequest, "invalid_password"
		if errors.Is(err, hsdb.ErrAccountPasswordReused) {
			code = "password_reused"
		} else if errors.Is(err, hsdb.ErrAccountConcurrentUpdate) {
			status, code = http.StatusConflict, "account_changed"
		}
		writeScaleForgeError(writer, status, code, err.Error())
		return
	}

	writer.WriteHeader(http.StatusNoContent)
}

func (api *scaleForgeAPI) listAccounts(writer http.ResponseWriter, req *http.Request) {
	if _, _, ok := api.authenticatedSession(writer, req, true, false); !ok {
		return
	}
	accounts, err := api.headscale.state.ListAccounts()
	if err != nil {
		log.Error().Err(err).Msg("listing ScaleForge accounts")
		writeScaleForgeError(writer, http.StatusInternalServerError, "internal_error", "unable to list accounts")
		return
	}

	response := make([]scaleForgeAccountResponse, 0, len(accounts))
	for idx := range accounts {
		response = append(response, scaleForgeAccountDTO(&accounts[idx]))
	}
	writeScaleForgeJSON(writer, http.StatusOK, response)
}

func (api *scaleForgeAPI) createAccount(writer http.ResponseWriter, req *http.Request) {
	session, _, ok := api.authenticatedSession(writer, req, true, false)
	if !ok {
		return
	}
	var createRequest struct {
		Username  string     `json:"username"`
		Password  string     `json:"password"`
		GroupID   *uint      `json:"groupId"`
		Role      string     `json:"role"`
		Enabled   *bool      `json:"enabled"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !decodeScaleForgeJSON(writer, req, &createRequest) {
		return
	}

	enabled := true
	if createRequest.Enabled != nil {
		enabled = *createRequest.Enabled
	}
	account, err := api.headscale.state.CreateAccount(hsdb.CreateAccountParams{
		Username:              createRequest.Username,
		Password:              createRequest.Password,
		GroupID:               createRequest.GroupID,
		Role:                  createRequest.Role,
		Enabled:               enabled,
		ExpiresAt:             createRequest.ExpiresAt,
		RequirePasswordChange: true,
		ActorAccountID:        &session.AccountID,
	})
	if err != nil {
		writeScaleForgeAccountMutationError(writer, err)
		return
	}
	if account.UserID != nil {
		account, err = api.headscale.state.GetAccountByID(account.ID)
		if err != nil {
			log.Error().Err(err).Msg("loading created ScaleForge account")
			writeScaleForgeError(writer, http.StatusInternalServerError, "internal_error", "account was created but could not be loaded")
			return
		}
	}

	writeScaleForgeJSON(writer, http.StatusCreated, scaleForgeAccountDTO(account))
}

func parseScaleForgeAccountID(writer http.ResponseWriter, req *http.Request) (uint, bool) {
	parsed, err := strconv.ParseUint(chi.URLParam(req, "accountID"), 10, 32)
	if err != nil || parsed == 0 {
		writeScaleForgeError(writer, http.StatusBadRequest, "invalid_account_id", "invalid account ID")
		return 0, false
	}

	return uint(parsed), true
}

func (api *scaleForgeAPI) updateAccount(writer http.ResponseWriter, req *http.Request) {
	session, _, ok := api.authenticatedSession(writer, req, true, false)
	if !ok {
		return
	}
	accountID, ok := parseScaleForgeAccountID(writer, req)
	if !ok {
		return
	}
	var updateRequest struct {
		Username       *string    `json:"username"`
		Role           *string    `json:"role"`
		Enabled        *bool      `json:"enabled"`
		ExpiresAt      *time.Time `json:"expiresAt"`
		ClearExpiresAt bool       `json:"clearExpiresAt"`
		GroupID        *uint      `json:"groupId"`
		ClearGroup     bool       `json:"clearGroup"`
	}
	if !decodeScaleForgeJSON(writer, req, &updateRequest) {
		return
	}

	account, changes, err := api.headscale.state.UpdateAccount(accountID, hsdb.UpdateAccountParams{
		Username:       updateRequest.Username,
		Role:           updateRequest.Role,
		Enabled:        updateRequest.Enabled,
		ExpiresAt:      updateRequest.ExpiresAt,
		ClearExpiresAt: updateRequest.ClearExpiresAt,
		GroupID:        updateRequest.GroupID,
		ClearGroup:     updateRequest.ClearGroup,
		ActorAccountID: &session.AccountID,
	}, time.Now().UTC())
	api.headscale.Change(changes...)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeScaleForgeError(writer, http.StatusNotFound, "account_not_found", "account not found")
		return
	}
	if err != nil {
		writeScaleForgeAccountMutationError(writer, err)
		return
	}

	writeScaleForgeJSON(writer, http.StatusOK, scaleForgeAccountDTO(account))
}

type scaleForgeGroupResponse struct {
	ID   uint   `json:"id"`
	Name string `json:"name"`
}

func (api *scaleForgeAPI) listAccountGroups(writer http.ResponseWriter, req *http.Request) {
	if _, _, ok := api.authenticatedSession(writer, req, true, false); !ok {
		return
	}
	groups, err := api.headscale.state.ListAccountGroups()
	if err != nil {
		log.Error().Err(err).Msg("listing ScaleForge account groups")
		writeScaleForgeError(writer, http.StatusInternalServerError, "internal_error", "unable to list groups")
		return
	}
	response := make([]scaleForgeGroupResponse, 0, len(groups))
	for idx := range groups {
		response = append(response, scaleForgeGroupResponse{ID: groups[idx].ID, Name: groups[idx].Name})
	}
	writeScaleForgeJSON(writer, http.StatusOK, response)
}

func (api *scaleForgeAPI) createAccountGroup(writer http.ResponseWriter, req *http.Request) {
	if _, _, ok := api.authenticatedSession(writer, req, true, false); !ok {
		return
	}
	var createRequest struct {
		Name string `json:"name"`
	}
	if !decodeScaleForgeJSON(writer, req, &createRequest) {
		return
	}
	group, err := api.headscale.state.CreateAccountGroup(createRequest.Name)
	if err != nil {
		if errors.Is(err, hsdb.ErrAccountGroupExists) {
			writeScaleForgeError(writer, http.StatusConflict, "group_exists", "group name already exists")
			return
		}
		writeScaleForgeError(writer, http.StatusBadRequest, "invalid_group", "group name is invalid")
		return
	}
	writeScaleForgeJSON(writer, http.StatusCreated, scaleForgeGroupResponse{ID: group.ID, Name: group.Name})
}

func (api *scaleForgeAPI) deleteAccountGroup(writer http.ResponseWriter, req *http.Request) {
	if _, _, ok := api.authenticatedSession(writer, req, true, false); !ok {
		return
	}
	parsed, err := strconv.ParseUint(chi.URLParam(req, "groupID"), 10, 32)
	if err != nil || parsed == 0 {
		writeScaleForgeError(writer, http.StatusBadRequest, "invalid_group_id", "invalid group ID")
		return
	}
	err = api.headscale.state.DeleteAccountGroup(uint(parsed))
	if errors.Is(err, hsdb.ErrAccountGroupInUse) {
		writeScaleForgeError(writer, http.StatusConflict, "group_in_use", "group still contains users")
		return
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		writeScaleForgeError(writer, http.StatusNotFound, "group_not_found", "group not found")
		return
	}
	if err != nil {
		log.Error().Err(err).Msg("deleting ScaleForge account group")
		writeScaleForgeError(writer, http.StatusInternalServerError, "internal_error", "unable to delete group")
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (api *scaleForgeAPI) resetAccountPassword(writer http.ResponseWriter, req *http.Request) {
	session, _, ok := api.authenticatedSession(writer, req, true, false)
	if !ok {
		return
	}
	accountID, ok := parseScaleForgeAccountID(writer, req)
	if !ok {
		return
	}
	var resetRequest struct {
		NewPassword string `json:"newPassword"`
	}
	if !decodeScaleForgeJSON(writer, req, &resetRequest) {
		return
	}

	changes, err := api.headscale.state.ResetAccountPassword(
		accountID,
		resetRequest.NewPassword,
		time.Now().UTC(),
		session.AccountID,
	)
	api.headscale.Change(changes...)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			writeScaleForgeError(writer, http.StatusNotFound, "account_not_found", "account not found")
			return
		}
		status, code := http.StatusBadRequest, "invalid_password"
		if errors.Is(err, hsdb.ErrAccountPasswordReused) {
			code = "password_reused"
		} else if errors.Is(err, hsdb.ErrAccountConcurrentUpdate) {
			status, code = http.StatusConflict, "account_changed"
		}
		writeScaleForgeError(writer, status, code, err.Error())
		return
	}

	writer.WriteHeader(http.StatusNoContent)
}

func normalizeRuntimeDNSConfig(config types.RuntimeDNSConfig) (types.RuntimeDNSConfig, error) {
	if len(config.GlobalNameservers) > 16 {
		return types.RuntimeDNSConfig{}, errors.New("at most 16 global nameservers are allowed")
	}
	if len(config.SearchDomains) > 32 {
		return types.RuntimeDNSConfig{}, errors.New("at most 32 search domains are allowed")
	}

	nameservers := make([]string, 0, len(config.GlobalNameservers))
	seenNameservers := make(map[string]struct{}, len(config.GlobalNameservers))
	for _, value := range config.GlobalNameservers {
		value = strings.TrimSpace(value)
		if value == "" || len(value) > 2048 {
			return types.RuntimeDNSConfig{}, errors.New("invalid global nameserver")
		}

		normalized := ""
		if addr, err := netip.ParseAddr(value); err == nil {
			normalized = addr.String()
		} else {
			parsed, err := url.Parse(value)
			if err != nil || !strings.EqualFold(parsed.Scheme, "https") || parsed.Hostname() == "" ||
				parsed.User != nil || parsed.Fragment != "" {
				return types.RuntimeDNSConfig{}, fmt.Errorf("invalid global nameserver %q", value)
			}
			normalized = parsed.String()
		}
		if _, exists := seenNameservers[normalized]; exists {
			continue
		}
		seenNameservers[normalized] = struct{}{}
		nameservers = append(nameservers, normalized)
	}
	if config.OverrideLocalDNS && len(nameservers) == 0 {
		return types.RuntimeDNSConfig{}, errors.New("overrideLocalDNS requires at least one global nameserver")
	}

	searchDomains := make([]string, 0, len(config.SearchDomains))
	seenDomains := make(map[string]struct{}, len(config.SearchDomains))
	for _, value := range config.SearchDomains {
		fqdn, err := dnsname.ToFQDN(strings.TrimSpace(value))
		if err != nil {
			return types.RuntimeDNSConfig{}, fmt.Errorf("invalid search domain %q", value)
		}
		normalized := strings.ToLower(fqdn.WithoutTrailingDot())
		if _, exists := seenDomains[normalized]; exists {
			continue
		}
		seenDomains[normalized] = struct{}{}
		searchDomains = append(searchDomains, normalized)
	}

	config.GlobalNameservers = nameservers
	config.SearchDomains = searchDomains

	return config, nil
}

func (api *scaleForgeAPI) dnsResponse(config types.RuntimeDNSConfig) map[string]any {
	return map[string]any{
		"magicDNS":          config.MagicDNS,
		"baseDomain":        api.headscale.cfg.DNSConfig.BaseDomain,
		"overrideLocalDNS":  config.OverrideLocalDNS,
		"globalNameservers": config.GlobalNameservers,
		"searchDomains":     config.SearchDomains,
	}
}

func (api *scaleForgeAPI) getDNS(writer http.ResponseWriter, req *http.Request) {
	if _, _, ok := api.authenticatedSession(writer, req, true, false); !ok {
		return
	}

	writeScaleForgeJSON(writer, http.StatusOK, api.dnsResponse(api.headscale.state.RuntimeDNSConfig()))
}

func (api *scaleForgeAPI) updateDNS(writer http.ResponseWriter, req *http.Request) {
	if _, _, ok := api.authenticatedSession(writer, req, true, false); !ok {
		return
	}

	var config types.RuntimeDNSConfig
	if !decodeScaleForgeJSON(writer, req, &config) {
		return
	}
	normalized, err := normalizeRuntimeDNSConfig(config)
	if err != nil {
		writeScaleForgeError(writer, http.StatusBadRequest, "invalid_dns_config", err.Error())
		return
	}
	if err := api.headscale.state.SetRuntimeDNSConfig(normalized); err != nil {
		log.Error().Err(err).Msg("updating runtime DNS configuration")
		writeScaleForgeError(writer, http.StatusInternalServerError, "internal_error", "unable to update DNS configuration")
		return
	}

	api.headscale.Change(change.DNSConfig())
	writeScaleForgeJSON(writer, http.StatusOK, api.dnsResponse(normalized))
}
