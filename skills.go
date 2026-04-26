package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var safeSkillNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func IsSafeSkillName(name string) bool {
	return name != "" && len(name) <= 80 && safeSkillNameRE.MatchString(name) && name != "." && name != ".."
}

func AvailableSkills(cfg Config) ([]string, error) {
	root, err := expandPath(cfg.SkillsDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() || !IsSafeSkillName(e.Name()) {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, e.Name(), "SKILL.md")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

func ValidateSkillExists(cfg Config, name string) error {
	if !IsSafeSkillName(name) {
		return fmt.Errorf("invalid skill name")
	}
	root, err := expandPath(cfg.SkillsDir)
	if err != nil {
		return err
	}
	info, err := os.Lstat(filepath.Join(root, name, "SKILL.md"))
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("SKILL.md is not a regular file")
	}
	return nil
}

func ValidateAgentsFile(cfg Config) error {
	path, err := expandPath(cfg.AgentsFile)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("AGENTS.md is not a regular file")
	}
	return nil
}

func PrepareContextCodexAssets(ctx context.Context, db *sql.DB, cfg Config, c Context) error {
	codexDir := ContextCodexDir(cfg, c.ID)
	skillsDir := filepath.Join(codexDir, "skills")
	if err := os.RemoveAll(skillsDir); err != nil {
		return err
	}
	if err := os.MkdirAll(skillsDir, 0o700); err != nil {
		return err
	}
	enabled, err := ListContextSkills(ctx, db, c.ID)
	if err != nil {
		return err
	}
	sourceRoot, err := expandPath(cfg.SkillsDir)
	if err != nil {
		return err
	}
	for _, name := range enabled {
		if !IsSafeSkillName(name) {
			continue
		}
		src := filepath.Join(sourceRoot, name)
		dst := filepath.Join(skillsDir, name)
		if err := copyTreeNoSymlinks(src, dst); err != nil {
			return fmt.Errorf("copy skill %s: %w", name, err)
		}
	}
	agentsDst := filepath.Join(codexDir, "AGENTS.md")
	if !c.AgentsEnabled {
		if err := os.Remove(agentsDst); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	agentsSrc, err := expandPath(cfg.AgentsFile)
	if err != nil {
		return err
	}
	return copyRegularFile(agentsSrc, agentsDst, 0o600)
}

func copyTreeNoSymlinks(src, dst string) error {
	rootInfo, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("skill root must be a real directory")
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink not allowed: %s", path)
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o700)
		}
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			return fmt.Errorf("path escapes skill root")
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("unsupported skill file type: %s", path)
		}
		return copyRegularFile(path, target, 0o600)
	})
}

func copyRegularFile(src, dst string, perm os.FileMode) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file")
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
