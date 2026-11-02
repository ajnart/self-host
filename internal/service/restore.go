package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/ajnart/self-host/internal/app"
	"github.com/ajnart/self-host/internal/backup"
	"github.com/ajnart/self-host/internal/compose"
	"github.com/ajnart/self-host/internal/events"
	"github.com/ajnart/self-host/internal/helpers"
	"github.com/ajnart/self-host/internal/host"
	"github.com/ajnart/self-host/internal/project"
	ts "github.com/ajnart/self-host/internal/tailscale"
)

type restoration struct {
	s               *Service
	file            string
	r               *backup.Reader
	targets         []string
	dockerApps      map[string]bool
	all             bool
	staging         *project.Staging
	scope           string
	startup         *project.RestoreStartup
	nodes           []string
	runningTargets  []string
	fresh           bool
	replaced        bool
	keepRecovery    bool
	originalCompose []byte
	originalEnv     []byte
	restoreEnv      map[string]string
}

// Restore puts Apps back from a Backup: every App in it when ids is empty.
// Other Apps are not touched. Restoring onto a new host recreates the Project.
func (s *Service) Restore(ctx context.Context, file string, ids []string, passphrase string) (result error) {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	r, err := backup.Open(f, passphrase)
	if err != nil {
		return err
	}
	defer r.Close()
	rs, err := s.newRestoration(ctx, file, r, ids)
	if err != nil {
		return err
	}
	defer func() {
		if err := rs.cleanup(ctx); err != nil {
			result = errors.Join(result, err)
		}
	}()
	if err := rs.extract(); err != nil {
		return err
	}
	if err := rs.verifyArchive(ctx); err != nil {
		return err
	}
	err = rs.apply(ctx)
	if err != nil && rs.replaced {
		rs.keepRecovery = true
		return fmt.Errorf("restore could not finish; original data and records retained in %s: %w", rs.staging.Dir(), err)
	}
	if err == nil {
		if stale := rs.staging.Stale(); len(stale) > 0 {
			rs.s.Log.Emit(events.Event{Event: events.Info, Step: "restore", Msg: "kept " + strings.Join(stale, ", ") + ", left by an earlier failed restore",
				Hint: "They hold the data and records from before that restore; delete them once you no longer need them."})
		}
	}
	return err
}

func validBackupAppID(id string) error {
	if id == "" || id == "." || !filepath.IsLocal(id) || filepath.Base(id) != id || strings.ContainsAny(id, "/\\\x00\r\n") {
		return fmt.Errorf("invalid App ID in backup: %q", id)
	}
	return nil
}

func (s *Service) newRestoration(ctx context.Context, file string, r *backup.Reader, ids []string) (*restoration, error) {
	// Manifest and target IDs enter mount and cleanup paths. Validate them
	// before preparing helpers, creating staging, or registering cleanup.
	seen := map[string]bool{}
	for _, app := range r.Manifest.Apps {
		if err := validBackupAppID(app.ID); err != nil {
			return nil, err
		}
		if seen[app.ID] {
			return nil, fmt.Errorf("backup lists App %s more than once", app.ID)
		}
		seen[app.ID] = true
	}
	for _, id := range ids {
		if err := validBackupAppID(id); err != nil {
			return nil, err
		}
	}
	targets := slices.Clone(ids)
	slices.Sort(targets)
	targets = slices.Compact(targets)
	if len(targets) == 0 {
		for _, a := range r.Manifest.Apps {
			targets = append(targets, a.ID)
		}
	}
	for _, id := range targets {
		if !r.Manifest.Has(id) {
			return nil, fmt.Errorf("%s is not in this backup", id)
		}
	}
	rs := &restoration{s: s, file: file, r: r, targets: targets, all: len(targets) == len(r.Manifest.Apps), dockerApps: map[string]bool{}}
	for _, app := range r.Manifest.Apps {
		if app.OwnershipNamespace == backup.OwnershipDocker && slices.Contains(targets, app.ID) {
			rs.dockerApps[app.ID] = true
		}
	}
	if len(rs.dockerApps) > 0 {
		if err := s.prepareBackupHelper(ctx); err != nil {
			return nil, err
		}
	}
	p, err := s.Project()
	if err != nil {
		return nil, err
	}
	rs.fresh = !p.Exists()
	if rs.fresh && !rs.all {
		return nil, fmt.Errorf("no Project yet; restore the whole backup first (self-host restore %s)", file)
	}
	if err := p.MkdirAll(); err != nil {
		return nil, err
	}
	rs.staging, err = p.NewStaging(r.Manifest.Folder)
	if err != nil {
		return nil, err
	}
	return rs, nil
}

func (rs *restoration) cleanup(ctx context.Context) error {
	if rs.keepRecovery {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	var cleanupErrors []error
	for _, id := range rs.targets {
		if !rs.dockerApps[id] {
			continue
		}
		for _, dir := range []string{rs.staging.AppDir(id), rs.staging.ReplacedDir(id)} {
			if err := rs.s.removeDockerAppDirectory(cleanup, dir); err != nil {
				cleanupErrors = append(cleanupErrors, err)
			}
		}
	}
	if err := errors.Join(cleanupErrors...); err != nil {
		return fmt.Errorf("restore cleanup failed; data retained in %s: %w", rs.staging.Dir(), err)
	}
	if err := rs.staging.Remove(); err != nil {
		return fmt.Errorf("restore cleanup failed; data retained in %s: %w", rs.staging.Dir(), err)
	}
	return nil
}

func (rs *restoration) extract() error {
	return rs.r.Extract(rs.staging.Dir(), func(name string) string {
		if name == ".env" || name == "secrets.env" || name == "state.json" || name == "compose.yml" || name == cloudflareRecordFile {
			return rs.staging.Path(name)
		}
		parts := strings.SplitN(name, "/", 3)
		if len(parts) >= 2 && parts[0] == rs.r.Manifest.Folder(parts[1]) && slices.Contains(rs.targets, parts[1]) {
			return rs.staging.Path(filepath.FromSlash(path.Clean(name)))
		}
		return ""
	}, rs.staging.ExtractChown(func(id string) bool { return rs.dockerApps[id] }))
}

func (rs *restoration) verifyArchive(ctx context.Context) error {
	// Reject an incomplete archive before stopping containers or replacing data.
	for _, id := range rs.targets {
		content, err := rs.readFragment(id)
		if err != nil {
			return fmt.Errorf("backup is missing a valid replacement for %s: %w", id, err)
		}
		var model struct {
			Services map[string]any `yaml:"services"`
		}
		if err := yaml.Unmarshal(content, &model); err != nil {
			return fmt.Errorf("backup has invalid Compose for %s: %w", id, err)
		}
		services := model.Services
		if len(services) == 0 {
			return fmt.Errorf("backup replacement for %s declares no services", id)
		}
		if _, err := os.Lstat(rs.staging.AppDir(id)); errors.Is(err, os.ErrNotExist) && rs.staging.Records().Canonical() && rs.r.Manifest.Folder(id) == "extra" {
			// A manual service in canonical YAML may have no local App files.
			if err := os.MkdirAll(rs.staging.AppDir(id), 0o700); err != nil {
				return err
			}
		}
	}
	// Validate safely extracted prerequisites before inspecting or removing
	// original containers. Runtime startup checks them again after replacement.
	for _, id := range rs.targets {
		a, found := rs.s.Catalog.App(id)
		if !found {
			continue
		}
		preflight, ok := a.(app.PreflightChecker)
		if !ok {
			continue
		}
		archived, err := rs.staging.Records().LoadState()
		if err != nil {
			return err
		}
		record := archived.Apps[id]
		if record == nil {
			return fmt.Errorf("backup is missing the configuration record for %s", id)
		}
		h := helpers.New(helpers.Config{App: id, Dir: rs.staging.AppDir(id), Inputs: publicInputs(a, record.Inputs), Log: rs.s.Log})
		if err := preflight.Preflight(ctx, h); err != nil {
			return fmt.Errorf("backup prerequisites for %s: %w", id, err)
		}
	}
	return nil
}

func (rs *restoration) readFragment(id string) ([]byte, error) {
	if rs.staging.Records().Canonical() {
		return rs.staging.Records().ReadFragment(id)
	}
	return rs.staging.ReadFragment(id)
}

func (rs *restoration) apply(ctx context.Context) error {
	// Only the initializer, called under the mutation lock, may claim fresh
	// recovery. Another command may have created the Project during extraction.
	rs.fresh = false
	absFile, err := filepath.Abs(rs.file)
	if err != nil {
		return err
	}
	identity, err := json.Marshal(struct {
		File     string
		Manifest backup.Manifest
		Targets  []string
	}{absFile, rs.r.Manifest, rs.targets})
	if err != nil {
		return err
	}
	rs.scope = fmt.Sprintf("%x", sha256.Sum256(identity))
	return rs.s.run(ctx, change{
		services:       func() []string { return rs.nodes },
		title:          "restore " + strings.Join(rs.targets, " ") + " from " + filepath.Base(rs.file),
		reconcileNames: true,
		namesTargets:   func() []string { return rs.targets },
		namesStart:     func(ss *session) bool { return rs.startup.Names && ss.namesOn() },
		complete:       func(ss *session) error { return ss.p.ClearRestoreStartup(rs.scope) },
		preflight:      rs.preflight,
		initialize:     rs.initialize,
		edit:           rs.edit,
		targets:        func(*session) ([]string, error) { return rs.runningTargets, nil },
		compose:        composeUp,
	})
}

func (rs *restoration) replacements(read func(id string) ([]byte, error)) (map[string]string, error) {
	replacements := map[string]string{}
	for _, id := range rs.targets {
		content, err := read(id)
		if err != nil {
			return nil, err
		}
		replacements[id] = string(content)
	}
	return replacements, nil
}

func (rs *restoration) initialize(p *project.Project) error {
	if !rs.all {
		return fmt.Errorf("no Project yet; restore the whole backup first (self-host restore %s)", rs.file)
	}
	settings, err := rs.staging.Records().LoadSettings()
	if err != nil {
		return err
	}
	if rs.staging.Records().Canonical() {
		if rs.s.Deps.Getenv("SELF_HOST_COMPOSE_PROJECT") == "" {
			name, err := rs.staging.Records().StoredComposeName()
			if err != nil {
				return err
			}
			if name != "" {
				p.ComposeName = name
			}
		}
		root, err := rs.staging.Records().ReadCompose()
		if err != nil {
			return err
		}
		if err := p.RestoreComposeBase(root); err != nil {
			return err
		}
		data, err := rs.staging.Records().ReadHomeFile(".env")
		if err != nil {
			return err
		}
		if err := p.WriteHomeFile(".env", data, 0o600); err != nil {
			return err
		}
		if rs.s.Deps.Getenv("SELF_HOST_COMPOSE_PROJECT") != "" {
			secrets, err := p.LoadSecrets()
			if err != nil {
				return err
			}
			if _, present := secrets["COMPOSE_PROJECT_NAME"]; present {
				secrets["COMPOSE_PROJECT_NAME"] = p.ComposeName
				if err := p.SaveSecrets(secrets); err != nil {
					return err
				}
			}
		}
	} else {
		if err := p.SaveSettings(settings); err != nil {
			return err
		}
	}
	rs.fresh = true
	return nil
}

func (rs *restoration) preflight(ctx context.Context, p *project.Project) error {
	archived := rs.staging.Records()
	settings, err := archived.LoadSettings()
	if err != nil {
		return err
	}
	if settings["MEDIA_SEPARATE_USERS"] == "on" {
		checkedPlatform := false
		reserved := map[int]bool{p.Owner.UID: true}
		var namespace *host.MediaNamespace
		for _, id := range rs.targets {
			if rs.r.Manifest.Folder(id) == project.ExtraDirName || !slices.Contains(mediaIdentityApps, id) {
				continue
			}
			if !checkedPlatform {
				var err error
				namespace, err = host.MediaUserNamespace(ctx, rs.s.Deps)
				if err != nil {
					return fmt.Errorf("restore separate media App users: %w", err)
				}
				if namespace != nil {
					reserved = map[int]bool{0: true}
					gid, err := strconv.Atoi(settings["PGID"])
					if err != nil || gid < 0 {
						return fmt.Errorf("PGID must be a mapped non-negative group before Restore")
					}
					if _, err := namespace.HostGID(gid); err != nil {
						return fmt.Errorf("restore shared media group: %w", err)
					}
				}
				checkedPlatform = true
			}
			if namespace != nil && !rs.dockerApps[id] {
				return fmt.Errorf("restore %s: this Backup records host file owners; an explicit ownership migration is required before restoring separate media App users into rootless Docker", id)
			}
			if namespace != nil {
				uid, err := strconv.Atoi(settings[mediaUIDKey(id)])
				if err != nil || uid <= 0 {
					return fmt.Errorf("restore %s: a positive mapped media file owner is required", id)
				}
				if _, err := namespace.HostUID(uid); err != nil {
					return fmt.Errorf("restore %s file owner: %w", id, err)
				}
			}
			if p.Exists() {
				continue // Existing Projects validate their persisted ownership below.
			}
			key := mediaUIDKey(id)
			uid, err := strconv.Atoi(settings[key])
			if err != nil || uid <= 0 || reserved[uid] {
				return fmt.Errorf("%s must be a positive file owner distinct from the Project owner and other restored media Apps; an explicit ownership migration is required before Restore", key)
			}
			reserved[uid] = true
			if err := host.CheckUnusedMediaUIDInNamespace(ctx, rs.s.Deps, uid, namespace); err != nil {
				return fmt.Errorf("restore %s: %w", key, err)
			}
		}
	}
	state, err := archived.LoadState()
	if err != nil {
		return err
	}
	for _, id := range rs.targets {
		if p.Exists() && slices.ContainsFunc(rs.r.Manifest.Apps, func(entry backup.ManifestApp) bool { return entry.ID == id && entry.Extra != p.IsExtra(id) }) {
			existing, err := p.AppIDs()
			if err != nil {
				return err
			}
			if slices.Contains(existing, id) {
				return fmt.Errorf("%s has different App folder ownership in this Project and the backup; migrate apps/extra explicitly before restoring", id)
			}
		}
		if rs.r.Manifest.Folder(id) != "extra" && state.Apps[id] == nil {
			return fmt.Errorf("backup is missing the configuration record for %s", id)
		}
	}
	if p.Exists() {
		currentSettings, err := p.LoadSettings()
		if err != nil {
			return err
		}
		if err := rs.validateMediaSettings(settings, currentSettings); err != nil {
			return err
		}
		settings = currentSettings
		current, err := p.LoadState()
		if err != nil {
			return err
		}
		for _, id := range rs.targets {
			if st := state.Apps[id]; st != nil {
				current.Apps[id] = st
			}
		}
		state = current
	}
	if err := rs.s.validateNames(ctx, settings, state); err != nil {
		return err
	}
	replacements, err := rs.replacements(rs.readFragment)
	if err != nil {
		return err
	}
	if err := p.ValidateServiceOwnership(replacements); err != nil {
		return err
	}
	if err := rs.planEnvironment(p, replacements); err != nil {
		return err
	}
	pending, err := p.LoadRestoreStartup()
	if err != nil {
		return err
	}
	if pending != nil {
		if pending.Scope != rs.scope {
			return fmt.Errorf("another Restore has pending startup; retry that approved Restore or run self-host down to cancel it")
		}
		rs.startup = pending
	} else {
		startup, err := rs.planStartup(ctx, p, settings, state)
		if err != nil {
			return err
		}
		if err := p.SaveRestoreStartup(*startup); err != nil {
			return err
		}
		rs.startup = startup
	}
	rs.runningTargets = slices.Clone(rs.startup.Targets)
	rs.nodes = slices.Clone(rs.startup.Services)
	return nil
}

func (rs *restoration) planEnvironment(p *project.Project, replacements map[string]string) error {
	rs.restoreEnv = map[string]string{}
	if !p.Exists() && rs.staging.Records().Canonical() {
		return nil // Fresh canonical Restore keeps the complete archived .env.
	}
	archived := rs.staging.Records()
	oldSettings, err := archived.LoadSettings()
	if err != nil {
		return err
	}
	oldSecrets, err := archived.LoadSecrets()
	if err != nil {
		return err
	}
	shared := map[string]bool{}
	ids, err := p.AppIDs()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if slices.Contains(rs.targets, id) {
			continue
		}
		data, err := p.ReadFragment(id)
		if err != nil {
			return err
		}
		keys, err := project.ComposeEnvironmentReferences(data)
		if err != nil {
			return err
		}
		keys, err = p.ExpandEnvironmentReferences(keys)
		if err != nil {
			return err
		}
		for key := range keys {
			shared[key] = true
		}
	}
	for _, id := range rs.targets {
		keys, err := project.ComposeEnvironmentReferences([]byte(replacements[id]))
		if err != nil {
			return err
		}
		keys, err = archived.ExpandEnvironmentReferences(keys)
		if err != nil {
			return err
		}
		for key, required := range keys {
			if _, public := oldSettings[key]; public || key == "ADMIN_USER" || key == "ADMIN_PASSWORD" || key == "COMPOSE_PROJECT_NAME" {
				continue // Host settings and the shared Admin login stay current.
			}
			value, found := oldSecrets[key]
			if !found {
				if required && rs.r.Manifest.Folder(id) == "extra" {
					return fmt.Errorf("backup lacks required Project environment assignment %s used by Unmanaged app %s; supply an explicit configuration migration before Restore", key, id)
				}
				continue
			}
			if shared[key] {
				matches, err := p.EnvironmentAssignmentMatches(archived, key)
				if err != nil {
					return err
				}
				if !matches {
					return fmt.Errorf("restoring %s would change Project environment assignment %s used by another App; restore the dependent Apps together or migrate the shared assignment explicitly", id, key)
				}
				continue // Keep the current shared assignment byte for byte.
			}
			rs.restoreEnv[key] = value
		}
	}
	return nil
}

// A saved Arr database records its container paths. Reusing it with another
// layout or file owner would be a migration, even when its YAML is valid.
func (rs *restoration) validateMediaSettings(archived, current map[string]string) error {
	value := func(settings map[string]string, key, fallback string) string {
		if settings[key] == "" {
			return fallback
		}
		return settings[key]
	}
	for _, id := range rs.targets {
		a, found := rs.s.Catalog.App(id)
		if !found || !a.Info().UsesMedia {
			continue
		}
		for key, fallback := range map[string]string{"MEDIA_LAYOUT": "legacy", "MEDIA_SEPARATE_USERS": "off"} {
			if value(archived, key, fallback) != value(current, key, fallback) {
				return fmt.Errorf("%s in the backup differs from this Project for media App %s; an explicit storage and ownership migration is required before Restore", key, id)
			}
		}
		if current["MEDIA_SEPARATE_USERS"] == "on" {
			for _, key := range []string{"PGID", mediaUIDKey(id)} {
				if archived[key] != current[key] {
					return fmt.Errorf("%s in the backup differs from this Project; migrate media App %s ownership explicitly before Restore", key, id)
				}
			}
		}
	}
	return nil
}

func (rs *restoration) planStartup(ctx context.Context, p *project.Project, settings map[string]string, state *project.State) (*project.RestoreStartup, error) {
	startup := &project.RestoreStartup{Scope: rs.scope, Fresh: !p.Exists(), Names: !p.Exists() && settings[settingNames] == namesOn}
	existing, err := p.AppIDs()
	if err != nil {
		return nil, err
	}
	var containers []compose.Container
	if p.Exists() {
		containers, err = rs.s.compose(p).Ps(ctx)
		if err != nil {
			return nil, fmt.Errorf("cannot determine running Apps before restore: %w", err)
		}
	}
	for _, id := range rs.targets {
		if !p.Exists() || !slices.Contains(existing, id) {
			startup.Targets = append(startup.Targets, id)
			continue
		}
		services, err := p.FragmentServices(id)
		if err != nil {
			return nil, err
		}
		if slices.ContainsFunc(containers, func(c compose.Container) bool { return c.State == "running" && slices.Contains(services, c.Service) }) {
			startup.Targets = append(startup.Targets, id)
		}
	}
	if !slices.Contains(rs.targets, tailscaleID) && state.Apps[tailscaleID] != nil && slices.ContainsFunc(containers, func(c compose.Container) bool { return c.Service == tailscaleID && c.State == "running" }) {
		for _, id := range startup.Targets {
			a, ok := rs.s.Catalog.App(id)
			if !ok || a.Info().NoWebUI || a.Info().HomeAddressOnly || a.Info().ContainerPort == 0 {
				continue
			}
			service := ts.ServiceName(id)
			if !slices.ContainsFunc(containers, func(c compose.Container) bool { return c.Service == service }) {
				startup.Services = append(startup.Services, service)
			}
		}
	}
	return startup, nil
}

func (rs *restoration) edit(ctx context.Context, ss *session) (result error) {
	if err := ss.restoreCloudflare(ctx, rs.staging, rs.targets); err != nil {
		return err
	}
	replacements, err := rs.replacements(rs.readFragment)
	if err != nil {
		return err
	}
	if err := ss.p.ValidateServiceOwnership(replacements); err != nil {
		return err
	}
	var containers []compose.Container
	if !rs.fresh {
		var err error
		containers, err = ss.compose.Ps(ctx)
		if err != nil {
			return fmt.Errorf("cannot determine running Apps before restore: %w", err)
		}
	}
	existing, err := ss.p.AppIDs()
	if err != nil {
		return err
	}
	oldApps := maps.Clone(ss.state.Apps)
	oldSecrets := maps.Clone(ss.secrets.values)
	rs.originalEnv, err = ss.p.ReadHomeFile(".env")
	if err != nil {
		return err
	}
	rs.originalCompose, err = ss.p.ReadCompose()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := rs.staging.SaveOriginalRecords(oldSecrets, ss.state, ss.settings); err != nil {
		return err
	}
	original := project.Project{Root: rs.staging.Path("original-records")}
	if err := original.WriteHomeFile(".env", rs.originalEnv, 0o600); err != nil {
		return err
	}
	if len(rs.originalCompose) > 0 {
		if err := original.WriteCompose(rs.originalCompose); err != nil {
			return err
		}
	}
	var recoverServices, recoverApps []string
	defer func() {
		if result == nil {
			return
		}
		if err := rs.rollback(ctx, ss, oldApps, oldSecrets, recoverServices, recoverApps); err != nil {
			result = errors.Join(result, err)
		}
	}()
	if err := rs.applyOwnership(ctx); err != nil {
		return err
	}
	for _, id := range rs.targets {
		if slices.Contains(existing, id) {
			services, err := ss.services(id)
			if err != nil {
				return err
			}
			// rm can partially succeed, so remember running services first.
			for _, container := range containers {
				if container.Service != cloudflareConnector && container.State == "running" && slices.Contains(services, container.Service) && !slices.Contains(recoverServices, container.Service) {
					recoverServices = append(recoverServices, container.Service)
					if !slices.Contains(recoverApps, id) {
						recoverApps = append(recoverApps, id)
					}
				}
			}
			if err := ss.compose.Remove(ctx, services...); err != nil {
				return err
			}
		}
		if err := rs.staging.Replace(ss.p, id); err != nil {
			return err
		}
	}
	for key, value := range rs.restoreEnv {
		ss.secrets.values[key] = value
	}
	registerSecrets(ss.s.Log, ss.secrets.values)
	if len(rs.restoreEnv) > 0 {
		keys := make([]string, 0, len(rs.restoreEnv))
		for key := range rs.restoreEnv {
			keys = append(keys, key)
		}
		if err := ss.p.RestoreEnvironmentAssignments(rs.staging.Records(), keys); err != nil {
			return err
		}
	}
	if err := ss.restoreRecords(rs.staging, rs.targets, rs.fresh || rs.startup.Fresh); err != nil {
		return err
	}
	for _, id := range rs.targets {
		if err := ss.p.RestoreFragment(id, replacements[id], rs.r.Manifest.Folder(id), rs.staging.Records().Canonical()); err != nil {
			return err
		}
		if st := ss.state.Apps[id]; st != nil {
			// Only a snapshot matching its archived generation may acquire a
			// new hash after rebasing. Otherwise Update must continue treating
			// the restored service as a manual edit, including old records that
			// had no generation hash at all.
			if st.FragmentSHA256 != "" && fragmentSum([]byte(replacements[id])) == st.FragmentSHA256 {
				data, err := ss.p.ReadFragment(id)
				if err != nil {
					return err
				}
				st.FragmentSHA256 = fragmentSum(data)
			} else {
				st.FragmentSHA256 = "restored-custom"
			}
		}
	}
	if _, err := ss.renderTailscale(); err != nil {
		return err
	}
	rs.replaced = true
	for _, id := range rs.targets {
		rs.s.Log.Emit(events.Event{Event: events.StepOK, App: id, Step: "restore", Msg: "restored " + rs.r.Manifest.Folder(id) + "/" + id + "/"})
	}
	return nil
}

func (rs *restoration) applyOwnership(ctx context.Context) error {
	for _, id := range rs.targets {
		if !rs.dockerApps[id] {
			continue
		}
		entries, err := rs.r.AppMetadata(id)
		if err != nil {
			return err
		}
		if err := rs.s.applyDockerMetadata(ctx, rs.staging.AppDir(id), entries); err != nil {
			return err
		}
	}
	return nil
}

func (rs *restoration) rollback(ctx context.Context, ss *session, oldApps map[string]*project.AppState, oldSecrets map[string]string, recoverServices, recoverApps []string) error {
	recoveryErrors := []error{rs.staging.Rollback(ss.p, func(id, dir string) error {
		if !rs.dockerApps[id] {
			return ss.p.RemoveAppDir(dir)
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		return rs.s.removeDockerAppDirectory(cleanup, dir)
	})}
	ss.state.Apps, ss.secrets.values = oldApps, oldSecrets
	if len(rs.originalCompose) > 0 {
		recoveryErrors = append(recoveryErrors, ss.p.WriteCompose(rs.originalCompose))
	}
	recoveryErrors = append(recoveryErrors, ss.p.SaveSecrets(oldSecrets), ss.p.SaveState(ss.state))
	if len(rs.originalEnv) > 0 {
		recoveryErrors = append(recoveryErrors, ss.p.WriteHomeFile(".env", rs.originalEnv, 0o600))
	}
	recoveryErrors = append(recoveryErrors, ss.p.WriteRoot())
	if err := errors.Join(recoveryErrors...); err != nil {
		rs.keepRecovery = true
		return fmt.Errorf("restore rollback failed; original data retained in %s: %w", rs.staging.Dir(), err)
	}
	if len(recoverServices) > 0 {
		recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
		defer cancel()
		if err := ss.compose.Up(recovery, recoverServices...); err != nil {
			rs.keepRecovery = true
			return fmt.Errorf("could not restart original Apps after rollback; records retained in %s; run self-host start %s: %w", rs.staging.Dir(), strings.Join(recoverApps, " "), err)
		}
	}
	return nil
}

// restoreRecords merges secrets and state for the restored Apps.
func (ss *session) restoreRecords(staging *project.Staging, targets []string, initialAdmin bool) error {
	backupProject := staging.Records()
	secrets, err := backupProject.LoadSecrets()
	if err != nil {
		return err
	}
	state, err := backupProject.LoadState()
	if err != nil {
		return err
	}
	if initialAdmin && ss.secrets.values["ADMIN_USER"] == "" && ss.secrets.values["ADMIN_PASSWORD"] == "" {
		ss.secrets.values["ADMIN_USER"] = secrets["ADMIN_USER"]
		ss.secrets.values["ADMIN_PASSWORD"] = secrets["ADMIN_PASSWORD"]
	}
	var changedAssignments []string
	for key, value := range secrets {
		if key == "ADMIN_USER" || key == "ADMIN_PASSWORD" {
			continue
		}
		for _, id := range targets {
			if strings.HasPrefix(key, SecretKey(id, "")) {
				if ss.secrets.values[key] != value {
					changedAssignments = append(changedAssignments, key)
				}
				ss.secrets.values[key] = value
			}
		}
	}
	if secrets["ADMIN_USER"] != ss.secrets.values["ADMIN_USER"] || secrets["ADMIN_PASSWORD"] != ss.secrets.values["ADMIN_PASSWORD"] {
		ss.s.Log.Emit(events.Event{Event: events.StepWarn, Step: "admin-login",
			Msg:  "the backup used a different Admin login than this Project",
			Hint: "Run self-host setup " + strings.Join(targets, " ") + " to switch the restored Apps to the current Admin login."})
	}
	registerSecrets(ss.s.Log, ss.secrets.values)
	if err := ss.p.RestoreEnvironmentAssignments(backupProject, changedAssignments); err != nil {
		return err
	}
	if err := ss.p.SaveSecrets(ss.secrets.values); err != nil {
		return err
	}
	for _, id := range targets {
		if st := state.Apps[id]; st != nil {
			ss.state.Apps[id] = st
		}
	}
	if err := ss.migratePrivateInputs(); err != nil {
		return err
	}
	return ss.p.SaveState(ss.state)
}
