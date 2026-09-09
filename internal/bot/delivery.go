package bot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FreshLabDev/tg"

	"searchy/internal/i18n"
	vidobridge "searchy/internal/vido"
)

// RunDeliveryWorker claims transport-neutral plans from core and executes only
// the whitelisted Telegram operations validated by Searchy.
func (h *Handlers) RunDeliveryWorker(ctx context.Context) {
	if h.vido == nil || !h.vido.Ready() {
		return
	}
	host, _ := os.Hostname()
	workerID := fmt.Sprintf("%s:%d", host, os.Getpid())
	h.log.Info("vido delivery worker started", "worker", workerID)
	defer h.log.Info("vido delivery worker stopped", "worker", workerID)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		delivery, err := h.vido.ClaimDelivery(ctx, workerID, 900)
		if err != nil {
			h.log.Warn("claim vido delivery failed", "err", err)
			if delivery != nil {
				rejectCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if rejectErr := h.vido.RejectDelivery(rejectCtx, workerID, delivery.JobID, "invalid_delivery_plan"); rejectErr != nil {
					h.log.Warn("reject undecodable vido plan failed", "job_id", delivery.JobID, "err", rejectErr)
				}
				cancel()
			}
		} else if delivery != nil {
			h.deliverPlan(ctx, workerID, delivery)
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunNotificationWorker durably delivers terminal processing status after a
// Searchy restart. Chat actions remain best-effort; failures and explicit
// delivery-unknown retry controls do not live only in process memory.
func (h *Handlers) RunNotificationWorker(ctx context.Context) {
	if h.vido == nil || !h.vido.Ready() {
		return
	}
	host, _ := os.Hostname()
	workerID := fmt.Sprintf("%s:%d:notify", host, os.Getpid())
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		n, err := h.vido.ClaimNotification(ctx, workerID, 120)
		if err != nil {
			h.log.Warn("claim vido notification failed", "err", err)
		} else if n != nil {
			if err := h.deliverNotification(ctx, n); err != nil {
				h.log.Warn("deliver vido notification failed", "job_id", n.JobID, "status", n.Status, "err", err)
			} else if err := h.ackNotificationWithRetry(ctx, workerID, n); err != nil {
				h.log.Warn("ack vido notification failed", "job_id", n.JobID, "err", err)
			}
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *Handlers) ackNotificationWithRetry(ctx context.Context, workerID string, n *vidobridge.Notification) error {
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		ackCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		last = h.vido.AckNotification(ackCtx, workerID, n.JobID, n.Status)
		cancel()
		if last == nil {
			return nil
		}
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt+1) * time.Second):
			}
		}
	}
	return last
}

func (h *Handlers) deliverNotification(ctx context.Context, n *vidobridge.Notification) error {
	lang := i18n.Resolve(n.Language)
	if n.Status == "delivery_unknown" {
		if n.OriginMessageID == 0 || n.RetryToken == "" {
			return errors.New("delivery_unknown notification is missing retry context")
		}
		return h.api.EditMessageReplyMarkup(ctx, n.TargetChatID, int64(n.OriginMessageID),
			&tg.InlineKeyboardMarkup{InlineKeyboard: [][]tg.InlineKeyboardButton{{{
				Text: i18n.T(lang, "download.retry_button"), CallbackData: retryCallbackPrefix + n.RetryToken,
			}}}})
	}
	_, err := h.api.SendReply(ctx, n.TargetChatID, 0, n.TargetThreadID,
		i18n.T(lang, searchyDownloadErrorKey(n.MessageKey)), nil)
	return err
}

func (h *Handlers) deliverPlan(ctx context.Context, workerID string, delivery *vidobridge.Delivery) {
	if err := vidobridge.ValidatePlan(delivery.Plan, delivery.JobID, h.sharedCacheRoot); err != nil {
		h.log.Warn("rejected vido delivery plan", "job_id", delivery.JobID, "err", err)
		if rejectErr := h.vido.RejectDelivery(ctx, workerID, delivery.JobID, "invalid_delivery_plan"); rejectErr != nil {
			h.log.Warn("record rejected vido plan failed", "job_id", delivery.JobID, "err", rejectErr)
		}
		return
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go h.renewDeliveryLease(heartbeatCtx, workerID, delivery.JobID)

	delivered := make(map[string]struct{}, len(delivery.DeliveredOps))
	for _, id := range delivery.DeliveredOps {
		delivered[id] = struct{}{}
	}

	for _, op := range delivery.Plan.Operations {
		if _, ok := delivered[op.OperationID]; ok {
			continue
		}
		if err := h.vido.BeginOperation(ctx, workerID, delivery.JobID, op); err != nil {
			h.log.Warn("begin vido Telegram operation failed", "job_id", delivery.JobID, "operation", op.OperationID, "err", err)
			return
		}
		messages, err := h.sendOperation(ctx, delivery, op)
		if err != nil {
			if sources := invalidFileIDSources(op, err); len(sources) > 0 {
				for _, source := range sources {
					_ = h.vido.InvalidateFileRef(ctx, source)
				}
				_ = h.vido.FailOperation(ctx, workerID, delivery.JobID, op, "invalid_file_id", false)
				return
			}
			unknown := !telegramDefiniteFailure(err)
			if failErr := h.vido.FailOperation(ctx, workerID, delivery.JobID, op, deliveryErrorReason(err), unknown); failErr != nil {
				h.log.Warn("record vido delivery failure failed", "job_id", delivery.JobID, "err", failErr)
			}
			h.log.Warn("vido Telegram operation failed", "job_id", delivery.JobID, "operation", op.OperationID, "unknown", unknown, "err", err)
			return
		}

		refs := operationFileRefs(op, messages)
		messageID := 0
		if len(messages) > 0 {
			messageID = int(messages[0].MessageID)
		}
		if err := h.ackOperationWithRetry(ctx, workerID, delivery.JobID, op, messageID, refs); err != nil {
			h.log.Warn("ack vido operation failed", "job_id", delivery.JobID, "operation", op.OperationID, "err", err)
			// begin_searchy_operation durably recorded "sending" before Telegram.
			// If ACK did not commit, lease recovery turns it into delivery_unknown;
			// if ACK did commit but its response was lost, the next claim observes
			// delivered and finishes without sending the media again.
			return
		}
	}
	if err := retryBridgeCommit(
		ctx,
		[]time.Duration{time.Second, 2 * time.Second},
		func(finishCtx context.Context) error {
			return h.vido.FinishDelivery(finishCtx, workerID, delivery.JobID)
		},
	); err != nil {
		h.log.Warn("finish vido delivery failed", "job_id", delivery.JobID, "err", err)
	}
}

func (h *Handlers) renewDeliveryLease(ctx context.Context, workerID string, jobID int64) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := h.vido.RenewDelivery(renewCtx, workerID, jobID, 900)
			cancel()
			if err != nil {
				h.log.Warn("renew vido delivery lease failed", "job_id", jobID, "err", err)
				return
			}
		}
	}
}

func (h *Handlers) ackOperationWithRetry(ctx context.Context, workerID string, jobID int64, op vidobridge.Operation, messageID int, refs []vidobridge.FileRef) error {
	return retryBridgeCommit(
		ctx,
		[]time.Duration{time.Second, 2 * time.Second},
		func(ackCtx context.Context) error {
			return h.vido.AckOperation(
				ackCtx, workerID, jobID, op, messageID, refs,
			)
		},
	)
}

func retryBridgeCommit(
	ctx context.Context,
	retryDelays []time.Duration,
	commit func(context.Context) error,
) error {
	var last error
	for attempt := 0; ; attempt++ {
		commitCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		last = commit(commitCtx)
		cancel()
		if last == nil {
			return nil
		}
		if attempt >= len(retryDelays) {
			return last
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryDelays[attempt]):
		}
	}
}

func (h *Handlers) sendOperation(ctx context.Context, delivery *vidobridge.Delivery, op vidobridge.Operation) ([]tg.Message, error) {
	chatID, threadID := delivery.TargetChatID, delivery.TargetThreadID
	markup := operationMarkup(op.Buttons)
	// A plan's captions arrive in a field named caption_html and its text is
	// Vido's own; the client sends both as HTML, like everything else here.
	caption := tg.CaptionOptions{ThreadID: threadID, Caption: op.CaptionHTML, Markup: markup}
	send := func() ([]tg.Message, error) {
		switch op.Type {
		case "video":
			msg, err := h.api.SendVideo(ctx, chatID, inputFile(*op.Source), tg.VideoOptions{
				CaptionOptions:    caption,
				Width:             op.Width,
				Height:            op.Height,
				Duration:          op.Duration,
				SupportsStreaming: op.SupportsStreaming,
			})
			return oneMessage(msg, err), err
		case "photo":
			msg, err := h.api.SendPhoto(ctx, chatID, inputFile(*op.Source), caption)
			return oneMessage(msg, err), err
		case "audio":
			msg, err := h.api.SendAudio(ctx, chatID, inputFile(*op.Source), tg.AudioOptions{
				CaptionOptions: caption,
				Duration:       op.Duration,
				Performer:      op.Performer,
				Title:          op.Title,
			})
			return oneMessage(msg, err), err
		case "document":
			msg, err := h.api.SendDocument(ctx, chatID, inputFile(*op.Source), tg.DocumentOptions{
				CaptionOptions:              caption,
				DisableContentTypeDetection: op.DisableContentTypeDetection,
			})
			return oneMessage(msg, err), err
		case "media_group":
			media := make([]tg.InputMedia, 0, len(op.Media))
			for _, item := range op.Media {
				switch item.Type {
				case "photo":
					media = append(media, tg.InputMediaPhoto{Media: inputFile(item.Source)})
				case "video":
					media = append(media, tg.InputMediaVideo{Media: inputFile(item.Source), SupportsStreaming: true})
				case "document":
					media = append(media, tg.InputMediaDocument{Media: inputFile(item.Source), DisableContentTypeDetection: true})
				}
			}
			return h.api.SendMediaGroup(ctx, chatID, media, threadID)
		case "text":
			msg, err := h.api.SendTextWithPreview(ctx, chatID, threadID, op.Text,
				&tg.LinkPreviewOptions{IsDisabled: op.DisableWebPagePreview}, markup)
			return oneMessage(msg, err), err
		default:
			return nil, fmt.Errorf("operation type not validated")
		}
	}
	for {
		messages, err := send()
		if !tg.IsTooManyRequests(err) {
			return messages, err
		}
		wait := tg.RetryAfter(err)
		if wait <= 0 {
			wait = time.Second
		}
		h.log.Info("Telegram rate limit", "job_id", delivery.JobID, "operation", op.OperationID, "retry_after", wait.String())
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
}

// inputFile turns a validated plan source into what the client sends. A
// local_file_uri names a file in the shared cache that the local Bot API
// server mounts too, so the server opens it instead of receiving an upload —
// and the client refuses that against Telegram's own endpoint, where it could
// never work, instead of letting it come back as a bad file identifier.
func inputFile(source vidobridge.Source) tg.InputFile {
	if source.Kind == "local_file_uri" {
		return tg.InputFileLocal{Path: strings.TrimPrefix(source.Value, "file://")}
	}
	return tg.InputFileString{Data: source.Value}
}

func operationMarkup(buttons []vidobridge.Button) *tg.InlineKeyboardMarkup {
	if len(buttons) == 0 {
		// A nil keyboard means "send no reply_markup at all". An empty one
		// would serialize as reply_markup=null, which the local Bot API
		// rejects instead of ignoring.
		return nil
	}
	rows := make([][]tg.InlineKeyboardButton, 0, len(buttons))
	for _, button := range buttons {
		item := tg.InlineKeyboardButton{Text: button.Text}
		if button.Type == "audio" {
			item.CallbackData = audioCallbackPrefix + button.Token
		} else {
			item.URL = button.URL
		}
		rows = append(rows, []tg.InlineKeyboardButton{item})
	}
	return &tg.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// oneMessage wraps a single send's result. A failed send hands back a zero
// Message, which must never be read as a delivered one when the caller goes
// looking for a file_id in it.
func oneMessage(message tg.Message, err error) []tg.Message {
	if err != nil || message.MessageID == 0 {
		return nil
	}
	return []tg.Message{message}
}

func operationFileRefs(op vidobridge.Operation, messages []tg.Message) []vidobridge.FileRef {
	refs := make([]vidobridge.FileRef, 0, len(messages))
	if op.Type == "media_group" {
		for i, message := range messages {
			if i >= len(op.Media) {
				break
			}
			if ref, ok := messageFileRef(op.Media[i].Source, op.Media[i].Type, message); ok {
				refs = append(refs, ref)
			}
		}
		return refs
	}
	if op.Source != nil && len(messages) > 0 {
		if ref, ok := messageFileRef(*op.Source, op.Type, messages[0]); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

func messageFileRef(source vidobridge.Source, kind string, message tg.Message) (vidobridge.FileRef, bool) {
	if source.Kind != "local_file_uri" || source.ContentKey == "" || source.VariantKey == "" {
		return vidobridge.FileRef{}, false
	}
	ref := vidobridge.FileRef{ContentKey: source.ContentKey, VariantKey: source.VariantKey, SendKind: kind, ItemIndex: source.ItemIndex}
	switch kind {
	case "video":
		if message.Video != nil {
			ref.FileID, ref.FileUniqueID = message.Video.FileID, message.Video.FileUniqueID
		}
	case "photo":
		if n := len(message.Photo); n > 0 {
			ref.FileID, ref.FileUniqueID = message.Photo[n-1].FileID, message.Photo[n-1].FileUniqueID
		}
	case "audio":
		if message.Audio != nil {
			ref.FileID, ref.FileUniqueID = message.Audio.FileID, message.Audio.FileUniqueID
		}
	case "document":
		if message.Document != nil {
			ref.FileID, ref.FileUniqueID = message.Document.FileID, message.Document.FileUniqueID
		}
	}
	return ref, ref.FileID != ""
}

func invalidFileIDSources(op vidobridge.Operation, err error) []string {
	if !badRequest(err) {
		return nil
	}
	text := strings.ToLower(err.Error())
	if !strings.Contains(text, "file") || (!strings.Contains(text, "identifier") && !strings.Contains(text, "file_id")) {
		return nil
	}
	values := make([]string, 0, len(op.Media)+1)
	if op.Source != nil && op.Source.Kind == "telegram_file_id" {
		values = append(values, op.Source.Value)
	}
	for _, item := range op.Media {
		if item.Source.Kind == "telegram_file_id" {
			values = append(values, item.Source.Value)
		}
	}
	return values
}

// telegramDefiniteFailure reports an answer that says the send did not happen
// and would not happen on a retry either. Anything else — a dropped
// connection, a 5xx, a timeout — may still have been delivered, and the job has
// to be recorded as delivery_unknown rather than failed.
func telegramDefiniteFailure(err error) bool {
	var apiErr *tg.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.HTTPStatus() {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

func badRequest(err error) bool {
	var apiErr *tg.APIError
	return errors.As(err, &apiErr) && apiErr.HTTPStatus() == http.StatusBadRequest
}
