package cliproxy

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

func (s *Service) applyConfigUpdate(newCfg *config.Config) {
	s.applyConfigUpdateWithAuthSynthesis(context.Background(), newCfg, true)
}

func (s *Service) applyWatcherConfigUpdate(newCfg *config.Config) {
	s.applyConfigUpdateWithAuthSynthesis(context.Background(), newCfg, false)
}

type configCommit struct {
	cfg      *config.Config
	sequence uint64
}

type routingRuntimeState struct {
	strategy           string
	sessionAffinity    bool
	sessionAffinityTTL time.Duration
}

func normalizedRoutingRuntimeState(cfg *config.Config) routingRuntimeState {
	state := routingRuntimeState{
		strategy:           "round-robin",
		sessionAffinityTTL: time.Hour,
	}
	if cfg == nil {
		return state
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Routing.Strategy)) {
	case "weighted-round-robin", "weightedroundrobin", "wrr":
		state.strategy = "weighted-round-robin"
	case "fill-first", "fillfirst", "ff":
		state.strategy = "fill-first"
	}
	state.sessionAffinity = cfg.Routing.SessionAffinity
	if ttl := strings.TrimSpace(cfg.Routing.SessionAffinityTTL); ttl != "" {
		if parsed, errParse := time.ParseDuration(ttl); errParse == nil && parsed > 0 {
			state.sessionAffinityTTL = parsed
		}
	}
	return state
}

func newRoutingSelector(state routingRuntimeState) coreauth.Selector {
	var selector coreauth.Selector
	switch state.strategy {
	case "weighted-round-robin":
		selector = &coreauth.WeightedRoundRobinSelector{}
	case "fill-first":
		selector = &coreauth.FillFirstSelector{}
	default:
		selector = &coreauth.RoundRobinSelector{}
	}
	if state.sessionAffinity {
		selector = coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{
			Fallback: selector,
			TTL:      state.sessionAffinityTTL,
		})
	}
	return selector
}

func (s *Service) applyConfigUpdateWithAuthSynthesis(ctx context.Context, newCfg *config.Config, synthesizeConfigAuths bool) bool {
	commit := s.commitConfigUpdate(newCfg)
	if commit.cfg == nil {
		return false
	}
	return s.applyConfigRuntime(ctx, commit, synthesizeConfigAuths)
}

// commitConfigUpdate applies only in-memory configuration state. Runtime work that
// may block on plugins, models, storage, or networking is deliberately deferred.
func (s *Service) commitConfigUpdate(newCfg *config.Config) configCommit {
	if s == nil {
		return configCommit{}
	}

	s.configUpdateMu.Lock()
	defer s.configUpdateMu.Unlock()

	if newCfg == nil {
		s.cfgMu.RLock()
		newCfg = s.cfg
		s.cfgMu.RUnlock()
	}
	if newCfg == nil {
		return configCommit{}
	}
	if errValidate := newCfg.ValidateCredentialWeights(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected config update with invalid credential weights")
		return configCommit{}
	}

	if errValidate := newCfg.ValidateAccessIsolation(); errValidate != nil {
		log.WithError(errValidate).Warn("rejected invalid account isolation policy")
		return configCommit{}
	}
	if errValidate := s.validateNewPoolReferences(newCfg); errValidate != nil {
		log.WithError(errValidate).Warn("rejected unknown account pool member")
		return configCommit{}
	}

	s.cfgMu.Lock()
	s.cfg = newCfg
	s.cfgMu.Unlock()
	s.configSequence++
	return configCommit{cfg: newCfg, sequence: s.configSequence}
}

func (s *Service) configCommitCurrent(commit configCommit) bool {
	if s == nil || commit.sequence == 0 {
		return false
	}
	s.configUpdateMu.Lock()
	current := s.configSequence == commit.sequence
	s.configUpdateMu.Unlock()
	return current
}

func (s *Service) applyConfigRuntime(ctx context.Context, commit configCommit, synthesizeConfigAuths bool) bool {
	cfg := commit.cfg
	if s == nil || cfg == nil {
		return false
	}
	s.configRuntimeMu.Lock()
	defer s.configRuntimeMu.Unlock()
	if !s.configCommitCurrent(commit) {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}

	if !s.applyManagerConfig(ctx, commit) {
		return false
	}
	s.pruneUnsupportedRuntimeAuths(ctx)
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if !s.applyPprofConfigContext(ctx, cfg) {
		return false
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if !s.updateServerClientsContext(ctx, cfg) {
		return false
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}

	// Access providers and handlers above must see the current configuration, but
	// unchanged provider runtimes must retain their established execution sessions.
	if onlyClientAccessConfigChanged(s.appliedRuntimeConfig, cfg) {
		return true
	}

	registrationCtx := coreauth.WithSkipPersist(ctx)
	s.syncPluginRuntimeConfigForConfig(registrationCtx, cfg)
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	var auths []*coreauth.Auth
	if s.coreManager != nil {
		auths = s.coreManager.List()
	}
	s.registerAvailableExecutors(registrationCtx, executorRegistrationOptions{
		includeBaseline:   cfg.Home.Enabled,
		forceReplaceAuths: true,
		auths:             auths,
	})
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if synthesizeConfigAuths {
		s.registerConfigAPIKeyAuths(registrationCtx, cfg)
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	if s.coreManager != nil && !cfg.Home.Enabled && cfg.SaveCooldownStatus {
		if errRestoreCooldown := s.coreManager.RestoreCooldownStates(registrationCtx); errRestoreCooldown != nil && ctx.Err() == nil {
			log.Warnf("failed to restore cooldown state after config update: %v", errRestoreCooldown)
		}
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	s.syncPluginModelRuntime(registrationCtx)
	if ctx.Err() != nil {
		return false
	}
	// Management handlers may mutate their configuration before the watcher runs.
	// Compare against an independent, successfully applied snapshot, never s.cfg
	// or the preceding commit (which may have been superseded before application).
	s.appliedRuntimeConfig = cfg.CloneForRuntime()
	return true
}

func onlyClientAccessConfigChanged(previous, next *config.Config) bool {
	if previous == nil || next == nil {
		return false
	}
	// Home config commits also synchronize remote runtime state independently of
	// the local client-access settings, so retain their existing reload path.
	if previous.Home.Enabled || next.Home.Enabled {
		return false
	}
	// These runtime-only settings are omitted from the persisted YAML.
	if !reflect.DeepEqual(previous.Home, next.Home) || previous.CodexOptimizeMultiAgentV2 != next.CodexOptimizeMultiAgentV2 {
		return false
	}
	before, after := *previous, *next
	before.APIKeys, after.APIKeys = nil, nil
	before.CodexKey = append([]config.CodexKey(nil), previous.CodexKey...)
	after.CodexKey = append([]config.CodexKey(nil), next.CodexKey...)
	before.ClaudeKey = append([]config.ClaudeKey(nil), previous.ClaudeKey...)
	after.ClaudeKey = append([]config.ClaudeKey(nil), next.ClaudeKey...)
	for i := range before.CodexKey {
		before.CodexKey[i].PoolAccountID = ""
	}
	for i := range after.CodexKey {
		after.CodexKey[i].PoolAccountID = ""
	}
	for i := range before.ClaudeKey {
		before.ClaudeKey[i].PoolAccountID = ""
	}
	for i := range after.ClaudeKey {
		after.ClaudeKey[i].PoolAccountID = ""
	}
	before.AccessIsolation, after.AccessIsolation = config.AccessIsolation{}, config.AccessIsolation{}
	before.AllowAnonymous, after.AllowAnonymous = false, false
	// Use the persisted representation so a management save that round-trips nil
	// collections as empty collections does not falsely require a provider reload.
	// Every setting except the explicit client-access allowlist remains significant.
	beforeYAML, errBefore := yaml.Marshal(&before)
	afterYAML, errAfter := yaml.Marshal(&after)
	return errBefore == nil && errAfter == nil && bytes.Equal(beforeYAML, afterYAML)
}

func (s *Service) applyManagerConfig(ctx context.Context, commit configCommit) bool {
	if s == nil || s.coreManager == nil || commit.cfg == nil {
		return s != nil && commit.cfg != nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if errContext := ctx.Err(); errContext != nil {
		return false
	}
	routingState := normalizedRoutingRuntimeState(commit.cfg)
	if s.appliedRoutingState == nil || *s.appliedRoutingState != routingState {
		s.coreManager.SetSelector(newRoutingSelector(routingState))
		s.appliedRoutingState = &routingState
	}
	s.applyRetryConfig(commit.cfg)
	store := s.resolveCooldownStateStore(commit.cfg)
	if !s.coreManager.ApplyConfigWithCooldownStateStore(ctx, commit.cfg, store) {
		return false
	}
	s.coreManager.SetOAuthModelAlias(commit.cfg.OAuthModelAlias)
	return true
}

func (s *Service) updateServerClientsContext(ctx context.Context, cfg *config.Config) bool {
	if s == nil || cfg == nil || (ctx != nil && ctx.Err() != nil) {
		return false
	}
	if s.updateServerClientsContextFn != nil {
		return s.updateServerClientsContextFn(ctx, cfg)
	}
	if s.server == nil {
		return true
	}
	return s.server.UpdateClientsContext(ctx, cfg)
}

func (s *Service) reloadConfigFromWatcher() bool {
	if s == nil || s.watcher == nil {
		return false
	}
	return s.watcher.ReloadConfigIfChanged()
}

func (s *Service) registerConfigAPIKeyAuths(ctx context.Context, cfg *config.Config) {
	if s == nil || s.coreManager == nil || cfg == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	configSynth := synthesizer.NewConfigSynthesizer()
	auths, errSynthesize := configSynth.Synthesize(&synthesizer.SynthesisContext{
		Config:      cfg,
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		log.Warnf("failed to synthesize config API key auths: %v", errSynthesize)
		return
	}

	registrationCtx := coreauth.WithDeferredAPIKeyModelAliasRebuild(ctx)
	tasks := make([]modelRegistrationTask, 0, len(auths))
	needsAliasRebuild := false
	for _, auth := range auths {
		if !coreauth.IsConfigAPIKeyAuth(auth) {
			continue
		}
		if !s.supportsAuth(auth) {
			continue
		}
		prepared := s.prepareCoreAuthForModelRegistration(registrationCtx, auth)
		if prepared == nil {
			continue
		}
		needsAliasRebuild = true
		authForRegistration := prepared
		tasks = append(tasks, modelRegistrationTask{
			phase:    modelRegistrationPhaseConfigAPIKey,
			category: modelRegistrationCategory(authForRegistration),
			run: func(compatCache *openAICompatibilityRegistrationCache) {
				s.completeModelRegistrationForAuthWithCache(registrationCtx, authForRegistration, compatCache)
			},
		})
	}
	if needsAliasRebuild {
		s.coreManager.RefreshAPIKeyModelAlias()
	}
	s.runModelRegistrationTasks(registrationCtx, tasks)
}

func forceHomeRuntimeConfig(cfg *config.Config) {
	if cfg == nil {
		return
	}
	cfg.APIKeys = nil
	cfg.UsageStatisticsEnabled = true
	cfg.DisableCooling = true
	cfg.SaveCooldownStatus = false
	cfg.RemoteManagement.AllowRemote = false
	cfg.RemoteManagement.DisableControlPanel = true
	cfg.Plugins.StoreAuth = nil
}

func (s *Service) validateNewPoolReferences(next *config.Config) error {
	if len(next.AccessIsolation.Pools) == 0 {
		return nil
	}
	known := map[string]bool{}
	if s.coreManager != nil {
		for _, account := range s.coreManager.List() {
			if id := coreauth.AccountIdentity(account); id != "" {
				known[id] = true
			}
		}
	}
	native, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{Config: next, Now: time.Now(), IDGenerator: synthesizer.NewStableIDGenerator()})
	if err != nil {
		return err
	}
	for _, account := range native {
		if id := coreauth.AccountIdentity(account); id != "" {
			known[id] = true
		}
	}
	existing := map[string]bool{}
	s.cfgMu.RLock()
	if s.cfg != nil {
		for _, pool := range s.cfg.AccessIsolation.Pools {
			for _, id := range pool.Accounts {
				existing[pool.ID+"\x00"+id] = true
			}
		}
	}
	s.cfgMu.RUnlock()
	for _, pool := range next.AccessIsolation.Pools {
		for _, id := range pool.Accounts {
			if !known[id] && !existing[pool.ID+"\x00"+id] {
				return fmt.Errorf("pool %q contains an unknown account", pool.ID)
			}
		}
	}
	return nil
}
