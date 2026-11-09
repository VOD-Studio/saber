package automation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/execution"
	"rua.plus/saber/internal/memory"
)

// Skill 是当前授权记忆空间的真实技能包与使用遥测。
type Skill struct {
	Package  memory.SkillPackage `json:"package"`
	Created  time.Time           `json:"created_at"`
	LastUsed time.Time           `json:"last_used"`
	Count    int64               `json:"use_count"`
}

// SkillStore 复用技能服务的 CAS、审批和作用域，不直接访问其数据库。
type SkillStore struct {
	List   func(context.Context, chat.Identity) ([]Skill, error)
	Get    func(context.Context, chat.Identity, string) (memory.SkillPackage, error)
	Put    func(context.Context, chat.Identity, memory.SkillPackage, int64) error
	Remove func(context.Context, chat.Identity, string, int64) error
}

type curatorState struct {
	Owner        chat.Identity `json:"owner"`
	WorkDir      string        `json:"work_dir"`
	Paused       bool          `json:"paused"`
	LastRun      time.Time     `json:"last_run"`
	LastActivity time.Time     `json:"last_activity"`
	Report       any           `json:"report"`
}
type skillFlags struct {
	Managed bool   `json:"managed"`
	Pinned  bool   `json:"pinned"`
	Locked  bool   `json:"locked"`
	State   string `json:"state"`
}
type ledgerEntry struct {
	ID      int64                `json:"id"`
	Name    string               `json:"name"`
	Action  string               `json:"action"`
	At      time.Time            `json:"at"`
	Before  *memory.SkillPackage `json:"before,omitempty"`
	After   *memory.SkillPackage `json:"after,omitempty"`
	Reverts int64                `json:"reverts,omitempty"`
}
type curatorInternalKey struct{}

func curatorScope(i chat.Identity) string {
	scope, ok := memory.Space(i)
	if !ok {
		return ""
	}
	return scope.Key()
}
func (s *Service) flags(ctx context.Context, i chat.Identity, name string) (skillFlags, error) {
	var f skillFlags
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM automation_skill_flags WHERE owner=? AND name=?`, curatorScope(i), name).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return f, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &f)
	}
	return f, err
}
func (s *Service) setFlags(ctx context.Context, i chat.Identity, name string, f skillFlags) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_skill_flags(owner,name,data) VALUES(?,?,?) ON CONFLICT(owner,name) DO UPDATE SET data=excluded.data`, curatorScope(i), name, data)
	return err
}

// SkillWritable 是所有前台技能写入共用的锁定检查；内部恢复仍需要技能服务原有权限。
func (s *Service) SkillWritable(ctx context.Context, name string) error {
	if ctx.Value(curatorInternalKey{}) != nil {
		return nil
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("skill lock requires trusted identity")
	}
	f, err := s.flags(ctx, i, name)
	if err != nil {
		return err
	}
	if f.Locked {
		return errors.New("skill is locked; explicitly unlock before mutation")
	}
	return nil
}

// SkillDeletable 同时保护锁定、pin 和任何仍被计划引用的技能。
func (s *Service) SkillDeletable(ctx context.Context, name string) error {
	if ctx.Value(curatorInternalKey{}) != nil {
		return nil
	}
	if err := s.SkillWritable(ctx, name); err != nil {
		return err
	}
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("skill deletion requires trusted identity")
	}
	f, err := s.flags(ctx, i, name)
	if err != nil {
		return err
	}
	if f.Pinned {
		return errors.New("skill is pinned")
	}
	protected, err := s.protectedSkills(ctx, i)
	if err != nil {
		return err
	}
	if protected[name] {
		return errors.New("skill is referenced by an existing cron schedule")
	}
	return nil
}

func (s *Service) protectedSkills(ctx context.Context, i chat.Identity) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM automation_cron`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var data []byte
		var m cronMetadata
		if err = rows.Scan(&data); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(data, &m); err != nil {
			return nil, err
		}
		if curatorScope(m.Owner) == curatorScope(i) {
			for _, name := range m.Skills {
				out[name] = true
			}
		}
	}
	return out, rows.Err()
}
func (s *Service) curatorState(ctx context.Context, i chat.Identity) (curatorState, error) {
	var state curatorState
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM automation_curator WHERE owner=?`, curatorScope(i)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return curatorState{Owner: i, LastRun: time.Now(), LastActivity: time.Now(), Report: "deferred first run for full interval"}, nil
	}
	if err == nil {
		err = json.Unmarshal(data, &state)
	}
	return state, err
}
func (s *Service) saveCurator(ctx context.Context, state curatorState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_curator(owner,data) VALUES(?,?) ON CONFLICT(owner) DO UPDATE SET data=excluded.data`, curatorScope(state.Owner), data)
	return err
}
func (s *Service) observe(ctx context.Context) error {
	i, ok := chat.IdentityFromContext(ctx)
	if !ok {
		return errors.New("curator observation requires trusted identity")
	}
	state, err := s.curatorState(ctx, i)
	if err != nil {
		return err
	}
	if s.host.Workspace == nil {
		return nil
	}
	dir, err := s.host.Workspace(ctx)
	if err != nil {
		// 纯聊天身份没有工作区授权时跳过观察，不能因此拒绝本轮对话。
		return nil
	}
	state.Owner = i
	state.WorkDir = dir
	state.LastActivity = time.Now()
	return s.saveCurator(ctx, state)
}

func (s *Service) tickCurator(ctx context.Context, now time.Time, owners []chat.Identity) error {
	if !s.cfg.Curator.Enabled || s.bindings.Skills.List == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT data FROM automation_curator`)
	if err != nil {
		return err
	}
	var states []curatorState
	for rows.Next() {
		var data []byte
		var state curatorState
		if err = rows.Scan(&data); err != nil {
			break
		}
		if err = json.Unmarshal(data, &state); err != nil {
			break
		}
		states = append(states, state)
	}
	err = errors.Join(err, rows.Err(), rows.Close())
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var result error
	for _, state := range states {
		seen[curatorScope(state.Owner)] = true
		if state.Paused || now.Sub(state.LastRun) < time.Duration(s.cfg.Curator.IntervalHours)*time.Hour || now.Sub(state.LastActivity) < time.Duration(s.cfg.Curator.MinIdleHours*float64(time.Hour)) {
			continue
		}
		runCtx := execution.WithTask(chat.WithIdentity(ctx, state.Owner), 0, state.WorkDir)
		scope, valid := memory.Space(state.Owner)
		if !valid || state.WorkDir == "" {
			continue
		}
		if scope.Kind == memory.ScopeGroup && (s.host.IsAdmin == nil || !s.host.IsAdmin(runCtx)) {
			continue
		}
		idle, e := s.tasks.SessionIdle(runCtx, state.Owner.Session)
		if e != nil {
			result = errors.Join(result, e)
			continue
		}
		if !idle {
			continue
		}
		if e = s.host.Check(runCtx, "curator", false); e != nil {
			state.Report = e.Error()
			result = errors.Join(result, s.saveCurator(runCtx, state))
			continue
		}
		report, e := s.runCurator(runCtx, state.Owner, false, s.cfg.Curator.Consolidate)
		state.LastRun = now
		state.Report = report
		if e != nil {
			state.Report = map[string]any{"report": report, "error": e.Error()}
		}
		result = errors.Join(result, e, s.saveCurator(runCtx, state))
	}
	for _, i := range owners {
		if !seen[curatorScope(i)] {
			state, e := s.curatorState(ctx, i)
			if e == nil {
				e = s.saveCurator(ctx, state)
			}
			result = errors.Join(result, e)
		}
	}
	return result
}

func (s *Service) curate(ctx context.Context, i chat.Identity, args map[string]any) (any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.bindings.Skills.List == nil {
		return nil, errors.New("curator skill storage unavailable")
	}
	action := text(args, "action")
	name := text(args, "name")
	dry := boolean(args, "dry_run")
	store := s.bindings.Skills
	scope, valid := memory.Space(i)
	if !valid {
		return nil, errors.New("curator requires valid memory space")
	}
	if scope.Kind == memory.ScopeGroup && action != "status" && action != "list" && action != "list-unmanaged" && action != "list-archived" && action != "ledger" && (s.host.IsAdmin == nil || !s.host.IsAdmin(ctx)) {
		return nil, errors.New("shared-skill curator mutations require configured administrator")
	}
	switch action {
	case "status", "list", "list-unmanaged":
		skills, err := store.List(ctx, i)
		if err != nil {
			return nil, err
		}
		var out []map[string]any
		for _, skill := range skills {
			f, e := s.flags(ctx, i, skill.Package.Name)
			if e != nil {
				return nil, e
			}
			managed := f.Managed || managedSource(skill.Package.Provenance.Source)
			if action == "list-unmanaged" && managed {
				continue
			}
			out = append(out, map[string]any{"skill": skill, "flags": f, "managed": managed})
		}
		state, err := s.curatorState(ctx, i)
		return map[string]any{"skills": out, "state": state}, err
	case "pause", "resume":
		state, err := s.curatorState(ctx, i)
		if err != nil {
			return nil, err
		}
		state.Paused = action == "pause"
		return state, s.saveCurator(ctx, state)
	case "run", "prune":
		report, err := s.runCurator(ctx, i, dry, boolean(args, "consolidate") || s.cfg.Curator.Consolidate)
		if !dry {
			state, e := s.curatorState(ctx, i)
			if e != nil {
				return report, errors.Join(err, e)
			}
			state.LastRun = time.Now()
			state.Report = report
			err = errors.Join(err, s.saveCurator(ctx, state))
		}
		return report, err
	case "pin", "unpin", "lock", "unlock", "adopt":
		if _, err := store.Get(ctx, i, name); err != nil {
			return nil, err
		}
		f, err := s.flags(ctx, i, name)
		if err != nil {
			return nil, err
		}
		switch action {
		case "pin":
			f.Pinned = true
		case "unpin":
			f.Pinned = false
		case "lock":
			f.Locked = true
		case "unlock":
			f.Locked = false
		case "adopt":
			f.Managed = true
		}
		if dry {
			return f, nil
		}
		return f, s.setFlags(ctx, i, name, f)
	case "archive":
		if dry {
			return map[string]any{"would_archive": name}, nil
		}
		return s.archive(ctx, i, name)
	case "restore":
		return s.restore(ctx, i, name, dry)
	case "list-archived":
		return s.archives(ctx, i)
	case "backup":
		return s.backup(ctx, i)
	case "rollback":
		if entry := number(args, "id"); entry > 0 {
			return s.rollbackEntry(ctx, i, entry, dry)
		}
		return s.rollbackBackup(ctx, i, text(args, "backup_id"), dry)
	case "ledger":
		return s.ledger(ctx, i, name)
	case "purge":
		days := int(number(args, "days"))
		if days == 0 {
			days = s.cfg.Curator.ArchiveTTLDays
		}
		if days < 1 {
			return nil, errors.New("purge requires explicit positive archive TTL")
		}
		return s.purge(ctx, i, days, dry)
	default:
		return nil, fmt.Errorf("unsupported curator action %q", action)
	}
}
func managedSource(source string) bool {
	return source == "agent" || source == "background_review" || source == "curator"
}
func (s *Service) runCurator(ctx context.Context, i chat.Identity, dry, consolidate bool) (any, error) {
	skills, err := s.bindings.Skills.List(ctx, i)
	if err != nil {
		return nil, err
	}
	protected, err := s.protectedSkills(ctx, i)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	var transitions []map[string]any
	var candidates []Skill
	for _, skill := range skills {
		name := skill.Package.Name
		flags, e := s.flags(ctx, i, name)
		if e != nil {
			return nil, e
		}
		source := skill.Package.Provenance.Source
		if flags.Pinned || flags.Locked || protected[name] || source == "hub" || source == "catalog" {
			continue
		}
		managed := flags.Managed || managedSource(source) || (skill.Package.Provenance.Builtin && s.cfg.Curator.PruneBuiltins)
		if !managed {
			continue
		}
		if !skill.Package.Provenance.Builtin {
			candidates = append(candidates, skill)
		}
		anchor := skill.LastUsed
		if anchor.IsZero() {
			anchor = skill.Created
		}
		if anchor.IsZero() {
			anchor = skill.Package.Provenance.InstalledAt
		}
		if anchor.IsZero() {
			anchor = now
		}
		age := now.Sub(anchor)
		target := "active"
		if age >= time.Duration(s.cfg.Curator.StaleAfterDays)*24*time.Hour {
			target = "stale"
		}
		if age >= time.Duration(s.cfg.Curator.ArchiveAfterDays)*24*time.Hour {
			target = "archived"
		}
		if flags.State == "" {
			flags.State = "active"
		}
		if target == flags.State {
			continue
		}
		transitions = append(transitions, map[string]any{"name": name, "from": flags.State, "to": target})
		if dry {
			continue
		}
		if target == "archived" {
			if _, e = s.archive(ctx, i, name); e != nil {
				return map[string]any{"transitions": transitions}, e
			}
		} else {
			flags.State = target
			if e = s.setFlags(ctx, i, name, flags); e != nil {
				return nil, e
			}
		}
	}
	report := map[string]any{"dry_run": dry, "transitions": transitions, "checked": len(skills)}
	if consolidate && len(candidates) > 0 {
		if !dry {
			backup, e := s.backup(ctx, i)
			if e != nil {
				return report, e
			}
			report["backup"] = backup
		}
		var review any
		var e error
		if s.bindings.Consolidate != nil {
			review, e = s.bindings.Consolidate(ctx, candidates, dry)
		} else {
			review, e = s.consolidate(ctx, i, candidates, dry)
		}
		report["review"] = review
		return report, e
	}
	return report, nil
}

func (s *Service) recordLedger(ctx context.Context, i chat.Identity, name, action string, before, after *memory.SkillPackage, reverts int64) (int64, error) {
	entry := ledgerEntry{Name: name, Action: action, At: time.Now(), Before: before, After: after, Reverts: reverts}
	data, err := json.Marshal(entry)
	if err != nil {
		return 0, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO automation_ledger(owner,skill,created,data) VALUES(?,?,?,?)`, curatorScope(i), name, entry.At.UnixMilli(), data)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
func (s *Service) archive(ctx context.Context, i chat.Identity, name string) (any, error) {
	if err := s.SkillDeletable(ctx, name); err != nil {
		return nil, err
	}
	pkg, err := s.bindings.Skills.Get(ctx, i, name)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(pkg)
	if err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_archives(owner,name,data,archived) VALUES(?,?,?,?)`, curatorScope(i), name, encoded, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	entry, err := s.recordLedger(ctx, i, name, "archive", &pkg, nil, 0)
	if err != nil {
		return nil, err
	}
	if err = s.bindings.Skills.Remove(context.WithValue(ctx, curatorInternalKey{}, true), i, name, pkg.Version); err != nil {
		return map[string]any{"archive_saved": true, "ledger_id": entry}, err
	}
	flags, err := s.flags(ctx, i, name)
	if err != nil {
		return nil, err
	}
	flags.State = "archived"
	return map[string]any{"archived": name, "ledger_id": entry}, s.setFlags(ctx, i, name, flags)
}
func (s *Service) archivePackage(ctx context.Context, i chat.Identity, name string) (memory.SkillPackage, error) {
	var pkg memory.SkillPackage
	var data []byte
	err := s.db.QueryRowContext(ctx, `SELECT data FROM automation_archives WHERE owner=? AND name=?`, curatorScope(i), name).Scan(&data)
	if err == nil {
		err = json.Unmarshal(data, &pkg)
	}
	return pkg, err
}
func (s *Service) restore(ctx context.Context, i chat.Identity, name string, dry bool) (any, error) {
	pkg, err := s.archivePackage(ctx, i, name)
	if err != nil {
		return nil, err
	}
	if dry {
		return pkg, nil
	}
	if _, err = s.bindings.Skills.Get(ctx, i, name); err == nil {
		return nil, errors.New("active skill exists; restore would overwrite")
	}
	entry, err := s.recordLedger(ctx, i, name, "restore", nil, &pkg, 0)
	if err != nil {
		return nil, err
	}
	if err = s.bindings.Skills.Put(context.WithValue(ctx, curatorInternalKey{}, true), i, pkg, 0); err != nil {
		return nil, err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM automation_archives WHERE owner=? AND name=?`, curatorScope(i), name)
	if err != nil {
		return nil, err
	}
	f, err := s.flags(ctx, i, name)
	if err != nil {
		return nil, err
	}
	f.State = "active"
	return map[string]any{"restored": name, "ledger_id": entry}, s.setFlags(ctx, i, name, f)
}
func (s *Service) archives(ctx context.Context, i chat.Identity) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name,archived FROM automation_archives WHERE owner=? ORDER BY archived DESC`, curatorScope(i))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var name string
		var ms int64
		if err = rows.Scan(&name, &ms); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"name": name, "archived_at": time.UnixMilli(ms)})
	}
	return out, rows.Err()
}
func (s *Service) ledger(ctx context.Context, i chat.Identity, name string) ([]ledgerEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,data FROM automation_ledger WHERE owner=? AND (?='' OR skill=?) ORDER BY id DESC LIMIT 100`, curatorScope(i), name, name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []ledgerEntry
	for rows.Next() {
		var e ledgerEntry
		var data []byte
		if err = rows.Scan(&e.ID, &data); err != nil {
			return nil, err
		}
		id := e.ID
		if err = json.Unmarshal(data, &e); err != nil {
			return nil, err
		}
		e.ID = id
		out = append(out, e)
	}
	return out, rows.Err()
}
func (s *Service) backup(ctx context.Context, i chat.Identity) (string, error) {
	skills, err := s.bindings.Skills.List(ctx, i)
	if err != nil {
		return "", err
	}
	id, err := newID()
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(skills)
	if err != nil {
		return "", err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO automation_backups(id,owner,created,data) VALUES(?,?,?,?)`, id, curatorScope(i), time.Now().UnixMilli(), data)
	if err != nil {
		return "", err
	}
	keep := s.cfg.Curator.BackupKeep
	if keep > 0 {
		_, err = s.db.ExecContext(ctx, `DELETE FROM automation_backups WHERE owner=? AND id NOT IN (SELECT id FROM automation_backups WHERE owner=? ORDER BY created DESC LIMIT ?)`, curatorScope(i), curatorScope(i), keep)
	}
	return id, err
}
func (s *Service) rollbackEntry(ctx context.Context, i chat.Identity, id int64, dry bool) (any, error) {
	var data []byte
	var entry ledgerEntry
	if err := s.db.QueryRowContext(ctx, `SELECT data FROM automation_ledger WHERE owner=? AND id=?`, curatorScope(i), id).Scan(&data); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	current, err := s.bindings.Skills.Get(ctx, i, entry.Name)
	exists := err == nil
	if err != nil && !errors.Is(err, memory.ErrSkillNotFound) {
		return nil, err
	}
	if dry {
		return entry, nil
	}
	var before *memory.SkillPackage
	if exists {
		before = &current
	}
	safety, err := s.recordLedger(ctx, i, entry.Name, "rollback", before, entry.Before, id)
	if err != nil {
		return nil, err
	}
	internal := context.WithValue(ctx, curatorInternalKey{}, true)
	if entry.Before == nil {
		if exists {
			err = s.bindings.Skills.Remove(internal, i, entry.Name, current.Version)
		}
	} else {
		version := int64(0)
		if exists {
			version = current.Version
		}
		err = s.bindings.Skills.Put(internal, i, *entry.Before, version)
	}
	return map[string]any{"rolled_back": id, "safety_ledger_id": safety}, err
}
func (s *Service) rollbackBackup(ctx context.Context, i chat.Identity, id string, dry bool) (any, error) {
	var data []byte
	query := `SELECT data FROM automation_backups WHERE owner=? AND id=?`
	if id == "" {
		query = `SELECT data FROM automation_backups WHERE owner=? ORDER BY created DESC LIMIT 1`
	}
	var err error
	if id == "" {
		err = s.db.QueryRowContext(ctx, query, curatorScope(i)).Scan(&data)
	} else {
		err = s.db.QueryRowContext(ctx, query, curatorScope(i), id).Scan(&data)
	}
	if err != nil {
		return nil, err
	}
	var target []Skill
	if err = json.Unmarshal(data, &target); err != nil {
		return nil, err
	}
	if dry {
		return target, nil
	}
	safety, err := s.backup(ctx, i)
	if err != nil {
		return nil, err
	}
	current, err := s.bindings.Skills.List(ctx, i)
	if err != nil {
		return nil, err
	}
	versions := map[string]int64{}
	for _, skill := range current {
		versions[skill.Package.Name] = skill.Package.Version
	}
	internal := context.WithValue(ctx, curatorInternalKey{}, true)
	for _, skill := range target {
		if _, err = s.recordLedger(ctx, i, skill.Package.Name, "rollback", nil, &skill.Package, 0); err != nil {
			return nil, err
		}
		if err = s.bindings.Skills.Put(internal, i, skill.Package, versions[skill.Package.Name]); err != nil {
			return map[string]any{"safety_backup": safety}, err
		}
		delete(versions, skill.Package.Name)
	}
	for name, version := range versions {
		if err = s.bindings.Skills.Remove(internal, i, name, version); err != nil {
			return map[string]any{"safety_backup": safety}, err
		}
	}
	return map[string]any{"restored": len(target), "safety_backup": safety}, nil
}
func (s *Service) purge(ctx context.Context, i chat.Identity, days int, dry bool) (any, error) {
	archives, err := s.archives(ctx, i)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range archives {
		at, _ := entry["archived_at"].(time.Time)
		if time.Since(at) >= time.Duration(days)*24*time.Hour {
			names = append(names, entry["name"].(string))
		}
	}
	sort.Strings(names)
	if dry {
		return map[string]any{"would_purge": names}, nil
	}
	for _, name := range names {
		pkg, e := s.archivePackage(ctx, i, name)
		if e != nil {
			return nil, e
		}
		if _, e = s.recordLedger(ctx, i, name, "purge", &pkg, nil, 0); e != nil {
			return nil, e
		}
		if _, e = s.db.ExecContext(ctx, `DELETE FROM automation_archives WHERE owner=? AND name=?`, curatorScope(i), name); e != nil {
			return nil, e
		}
	}
	return map[string]any{"purged": names}, nil
}
