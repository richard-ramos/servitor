package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type TelegramClient struct {
	token  string
	base   string
	client *http.Client
}

func NewTelegramClient(token string) *TelegramClient {
	return &TelegramClient{
		token:  token,
		base:   "https://api.telegram.org/bot" + token,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

type tgResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

type Update struct {
	UpdateID int             `json:"update_id"`
	Message  TelegramMessage `json:"message"`
}

type TelegramMessage struct {
	MessageID       int              `json:"message_id"`
	MessageThreadID int              `json:"message_thread_id"`
	From            TelegramUser     `json:"from"`
	Chat            TelegramChat     `json:"chat"`
	Date            int64            `json:"date"`
	Text            string           `json:"text"`
	Caption         string           `json:"caption"`
	ReplyToMessage  *TelegramMessage `json:"reply_to_message"`
	Document        *TelegramFileObj `json:"document"`
	Audio           *TelegramFileObj `json:"audio"`
	Video           *TelegramFileObj `json:"video"`
	Voice           *TelegramFileObj `json:"voice"`
	Animation       *TelegramFileObj `json:"animation"`
	Photo           []TelegramPhoto  `json:"photo"`
}

type TelegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type TelegramChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type TelegramFileObj struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileName     string `json:"file_name"`
	MimeType     string `json:"mime_type"`
	FileSize     int64  `json:"file_size"`
}

type TelegramPhoto struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

type TelegramFileInfo struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	FileSize     int64  `json:"file_size"`
	FilePath     string `json:"file_path"`
}

func (c *TelegramClient) GetUpdates(ctx context.Context, offset int) ([]Update, error) {
	q := url.Values{}
	q.Set("timeout", "30")
	q.Set("allowed_updates", `["message"]`)
	if offset > 0 {
		q.Set("offset", strconv.Itoa(offset))
	}
	var resp tgResponse[[]Update]
	if err := c.get(ctx, "getUpdates", q, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("telegram getUpdates: %s", resp.Description)
	}
	return resp.Result, nil
}

func (c *TelegramClient) SendMessage(ctx context.Context, chatID int64, topicID int, text string) ([]TelegramMessage, error) {
	if text == "" {
		text = "(empty response)"
	}
	var sent []TelegramMessage
	for _, chunk := range splitTelegramMessage(text, 3900) {
		body := map[string]any{
			"chat_id": chatID,
			"text":    chunk,
		}
		if topicID != 0 {
			body["message_thread_id"] = topicID
		}
		var resp tgResponse[TelegramMessage]
		if err := c.postJSON(ctx, "sendMessage", body, &resp); err != nil {
			return sent, err
		}
		if !resp.OK {
			return sent, fmt.Errorf("telegram sendMessage: %s", resp.Description)
		}
		sent = append(sent, resp.Result)
	}
	return sent, nil
}

func (c *TelegramClient) SendDocument(ctx context.Context, chatID int64, topicID int, path string, caption string) (TelegramMessage, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("chat_id", strconv.FormatInt(chatID, 10)); err != nil {
		return TelegramMessage{}, err
	}
	if topicID != 0 {
		if err := writer.WriteField("message_thread_id", strconv.Itoa(topicID)); err != nil {
			return TelegramMessage{}, err
		}
	}
	if caption != "" {
		if err := writer.WriteField("caption", caption); err != nil {
			return TelegramMessage{}, err
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return TelegramMessage{}, err
	}
	defer file.Close()
	part, err := writer.CreateFormFile("document", filepath.Base(path))
	if err != nil {
		return TelegramMessage{}, err
	}
	if _, err := io.Copy(part, file); err != nil {
		return TelegramMessage{}, err
	}
	if err := writer.Close(); err != nil {
		return TelegramMessage{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/sendDocument", &body)
	if err != nil {
		return TelegramMessage{}, err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := c.client.Do(req)
	if err != nil {
		return TelegramMessage{}, err
	}
	defer resp.Body.Close()
	var out tgResponse[TelegramMessage]
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return TelegramMessage{}, err
	}
	if !out.OK {
		return TelegramMessage{}, fmt.Errorf("telegram sendDocument: %s", out.Description)
	}
	return out.Result, nil
}

func (c *TelegramClient) GetFile(ctx context.Context, fileID string) (TelegramFileInfo, error) {
	q := url.Values{}
	q.Set("file_id", fileID)
	var resp tgResponse[TelegramFileInfo]
	if err := c.get(ctx, "getFile", q, &resp); err != nil {
		return TelegramFileInfo{}, err
	}
	if !resp.OK {
		return TelegramFileInfo{}, fmt.Errorf("telegram getFile: %s", resp.Description)
	}
	return resp.Result, nil
}

func (c *TelegramClient) DownloadFile(ctx context.Context, filePath string, maxBytes int64) ([]byte, error) {
	u := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", c.token, filePath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("telegram file download status %d", resp.StatusCode)
	}
	var buf bytes.Buffer
	_, err = io.Copy(&buf, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(buf.Len()) > maxBytes {
		return nil, fmt.Errorf("attachment exceeds max size")
	}
	return buf.Bytes(), nil
}

func (c *TelegramClient) get(ctx context.Context, method string, q url.Values, out any) error {
	u := c.base + "/" + method
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *TelegramClient) postJSON(ctx context.Context, method string, body any, out any) error {
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(body); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func splitTelegramMessage(s string, max int) []string {
	var out []string
	for len(s) > max {
		cut := strings.LastIndex(s[:max], "\n")
		if cut < max/2 {
			cut = max
		}
		out = append(out, s[:cut])
		s = strings.TrimLeft(s[cut:], "\n")
	}
	out = append(out, s)
	return out
}

func TelegramSenderName(u TelegramUser) string {
	if u.Username != "" {
		return "@" + u.Username
	}
	name := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if name == "" {
		return strconv.FormatInt(u.ID, 10)
	}
	return name
}

type App struct {
	cfg      Config
	db       *sql.DB
	tg       *TelegramClient
	redactor *Redactor
	runner   *DockerRunner
}

func (a *App) PollLoop(ctx context.Context) error {
	offset := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		updates, err := a.tg.GetUpdates(ctx, offset)
		if err != nil {
			fmt.Printf("telegram poll error: %s\n", a.redactor.Redact(err.Error()))
			time.Sleep(3 * time.Second)
			continue
		}
		for _, upd := range updates {
			if upd.UpdateID >= offset {
				offset = upd.UpdateID + 1
			}
			if err := a.HandleUpdate(ctx, upd); err != nil {
				fmt.Printf("handle update %d: %s\n", upd.UpdateID, a.redactor.Redact(err.Error()))
			}
		}
	}
}

func (a *App) HandleUpdate(ctx context.Context, upd Update) error {
	seen, err := IsSeen(ctx, a.db, upd.UpdateID)
	if err != nil {
		return err
	}
	if seen {
		return nil
	}
	if err := MarkSeen(ctx, a.db, upd.UpdateID); err != nil {
		return err
	}
	msg := upd.Message
	if msg.MessageID == 0 {
		return nil
	}
	admin := a.cfg.AdminUserIDs[msg.From.ID]
	stored := StoredMessage{
		ChatID:            msg.Chat.ID,
		TopicID:           msg.MessageThreadID,
		TelegramMessageID: msg.MessageID,
		SenderID:          msg.From.ID,
		SenderName:        TelegramSenderName(msg.From),
		Text:              msg.Text,
		Caption:           msg.Caption,
		IsBot:             msg.From.IsBot,
		IsAdmin:           admin,
	}
	if msg.ReplyToMessage != nil {
		stored.ReplyToMessageID = msg.ReplyToMessage.MessageID
	}
	messageID, err := StoreMessage(ctx, a.db, stored)
	if err != nil {
		return err
	}
	if !admin {
		_, _ = a.reply(ctx, msg.Chat.ID, msg.MessageThreadID, "Unauthorized.")
		return nil
	}
	return a.dispatchMessage(ctx, msg, messageID)
}

func (a *App) reply(ctx context.Context, chatID int64, topicID int, text string) ([]TelegramMessage, error) {
	text = a.redactor.Redact(text)
	sent, err := a.tg.SendMessage(ctx, chatID, topicID, text)
	if err != nil {
		return sent, err
	}
	for _, msg := range sent {
		_, _ = StoreMessage(ctx, a.db, StoredMessage{
			ChatID:            msg.Chat.ID,
			TopicID:           msg.MessageThreadID,
			TelegramMessageID: msg.MessageID,
			SenderID:          msg.From.ID,
			SenderName:        "servitor",
			Text:              msg.Text,
			IsBot:             true,
			IsAdmin:           true,
		})
	}
	return sent, nil
}
