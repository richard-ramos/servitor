package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type attachmentCandidate struct {
	FileID       string `json:"file_id"`
	UniqueID     string `json:"unique_id"`
	Name         string `json:"name"`
	MimeType     string `json:"mime_type"`
	Size         int64  `json:"size"`
	Kind         string `json:"kind"`
	WorkspaceRel string `json:"workspace_rel_path,omitempty"`
}

func (a *App) storeAttachmentsForMessage(ctx context.Context, c Context, messageID int64, msg TelegramMessage) error {
	cands := attachmentCandidates(msg)
	if len(cands) == 0 {
		return nil
	}
	baseRel := filepath.Join("attachments", fmt.Sprintf("%d", msg.MessageID))
	baseAbs := filepath.Join(c.WorkspaceDir, baseRel)
	if err := os.MkdirAll(baseAbs, 0o700); err != nil {
		return err
	}
	var manifest []attachmentCandidate
	for i, cand := range cands {
		if cand.Size > a.cfg.MaxAttachmentBytes && cand.Size > 0 {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Attachment %s is too large.", cand.Name))
			continue
		}
		info, err := a.tg.GetFile(ctx, cand.FileID)
		if err != nil {
			return err
		}
		size := cand.Size
		if size == 0 {
			size = info.FileSize
		}
		if size > a.cfg.MaxAttachmentBytes && size > 0 {
			_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, fmt.Sprintf("Attachment %s is too large.", cand.Name))
			continue
		}
		data, err := a.tg.DownloadFile(ctx, info.FilePath, a.cfg.MaxAttachmentBytes)
		if err != nil {
			return err
		}
		name := SanitizeFilename(cand.Name)
		if name == "attachment" {
			name = fmt.Sprintf("%s_%d", cand.Kind, i+1)
		}
		rel := filepath.Join(baseRel, name)
		abs := filepath.Join(c.WorkspaceDir, rel)
		if err := os.WriteFile(abs, data, 0o600); err != nil {
			return err
		}
		if _, err := ValidateContextPath(a.cfg, c.ID, filepath.Dir(abs)); err != nil {
			return err
		}
		cand.WorkspaceRel = rel
		manifest = append(manifest, cand)
		if err := AddAttachment(ctx, a.db, Attachment{
			MessageID:        messageID,
			TelegramFileID:   cand.FileID,
			TelegramUniqueID: cand.UniqueID,
			LocalPath:        abs,
			WorkspaceRelPath: rel,
			MimeType:         cand.MimeType,
			OriginalFilename: cand.Name,
			SizeBytes:        int64(len(data)),
		}); err != nil {
			return err
		}
	}
	if len(manifest) > 0 {
		b, _ := json.MarshalIndent(manifest, "", "  ")
		if err := os.WriteFile(filepath.Join(baseAbs, "manifest.json"), b, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func attachmentCandidates(msg TelegramMessage) []attachmentCandidate {
	var out []attachmentCandidate
	addObj := func(kind string, f *TelegramFileObj) {
		if f == nil || f.FileID == "" {
			return
		}
		name := f.FileName
		if name == "" {
			name = kind
		}
		out = append(out, attachmentCandidate{FileID: f.FileID, UniqueID: f.FileUniqueID, Name: name, MimeType: f.MimeType, Size: f.FileSize, Kind: kind})
	}
	addObj("document", msg.Document)
	addObj("audio", msg.Audio)
	addObj("video", msg.Video)
	addObj("voice", msg.Voice)
	addObj("animation", msg.Animation)
	if len(msg.Photo) > 0 {
		best := msg.Photo[0]
		for _, p := range msg.Photo[1:] {
			if p.Width*p.Height > best.Width*best.Height {
				best = p
			}
		}
		out = append(out, attachmentCandidate{
			FileID:   best.FileID,
			UniqueID: best.FileUniqueID,
			Name:     fmt.Sprintf("photo_%dx%d.jpg", best.Width, best.Height),
			MimeType: "image/jpeg",
			Size:     best.FileSize,
			Kind:     "photo",
		})
	}
	return out
}
