package automation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sashabaranov/go-openai"
	"rua.plus/saber/internal/chat"
	"rua.plus/saber/internal/memory"
)

type reviewPatch struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Absorbs     []string `json:"absorbs"`
}

func (s *Service) consolidate(ctx context.Context, i chat.Identity, candidates []Skill, dry bool) (any, error) {
	if s.host.Complete == nil {
		return nil, errors.New("curator auxiliary model unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	payload, err := json.Marshal(candidates)
	if err != nil {
		return nil, err
	}
	raw, err := s.host.Complete(ctx, s.cfg.JudgeModel, []openai.ChatCompletionMessage{{Role: "system", Content: "Review only these explicitly curator-managed skill packages. Keep specific useful procedures; consolidate genuinely overlapping procedures into readable umbrella skills, or patch drift. Never remove useful details or change authority. Return JSON {\"patches\":[{\"name\":\"slug\",\"description\":\"trigger\",\"content\":\"complete markdown\",\"absorbs\":[\"old-name\"]}]}. Empty patches means keep all. Existing packages retain metadata/resources; new umbrellas must carry the complete absorbed procedures. No commands or external provider calls."}, {Role: "user", Content: string(payload)}})
	if err != nil {
		return nil, err
	}
	var plan struct {
		Patches []reviewPatch `json:"patches"`
	}
	if err = json.Unmarshal([]byte(stripFence(raw)), &plan); err != nil {
		return nil, err
	}
	if len(plan.Patches) > 128 {
		return nil, errors.New("curator review patch limit exceeded")
	}
	byName := map[string]Skill{}
	for _, skill := range candidates {
		byName[skill.Package.Name] = skill
	}
	seen := map[string]bool{}
	for _, patch := range plan.Patches {
		if patch.Name == "" || patch.Content == "" || seen[patch.Name] {
			return nil, errors.New("curator returned invalid/duplicate patch")
		}
		seen[patch.Name] = true
		for _, old := range patch.Absorbs {
			if _, ok := byName[old]; !ok || old == patch.Name {
				return nil, fmt.Errorf("absorption target %q is not an eligible source", old)
			}
		}
	}
	if dry {
		return plan, nil
	}
	var applied []map[string]any
	for _, patch := range plan.Patches {
		pkg := memory.SkillPackage{Name: patch.Name, Description: patch.Description, Content: patch.Content, Provenance: memory.SkillProvenance{Source: "curator", InstalledAt: time.Now()}}
		version := int64(0)
		var before *memory.SkillPackage
		if existing, ok := byName[patch.Name]; ok {
			original := existing.Package
			before = &original
			pkg = original
			pkg.Content = patch.Content
			if patch.Description != "" {
				pkg.Description = patch.Description
			}
			version = original.Version
		} else {
			if _, e := s.bindings.Skills.Get(ctx, i, patch.Name); e == nil {
				return applied, errors.New("curator cannot overwrite noncandidate skill")
			} else if !errors.Is(e, memory.ErrSkillNotFound) {
				return applied, e
			}
		}
		if err = s.SkillWritable(ctx, patch.Name); err != nil {
			return applied, err
		}
		entry, err := s.recordLedger(ctx, i, patch.Name, "consolidate", before, &pkg, 0)
		if err != nil {
			return applied, err
		}
		if err = s.bindings.Skills.Put(ctx, i, pkg, version); err != nil {
			return applied, err
		}
		flags, err := s.flags(ctx, i, patch.Name)
		if err != nil {
			return applied, err
		}
		flags.Managed = true
		flags.State = "active"
		if err = s.setFlags(ctx, i, patch.Name, flags); err != nil {
			return applied, err
		}
		row := map[string]any{"name": patch.Name, "ledger_id": entry}
		var absorbed []any
		for _, old := range patch.Absorbs {
			result, e := s.archive(ctx, i, old)
			if e != nil {
				return applied, e
			}
			absorbed = append(absorbed, result)
		}
		row["absorbed"] = absorbed
		applied = append(applied, row)
	}
	return applied, nil
}
