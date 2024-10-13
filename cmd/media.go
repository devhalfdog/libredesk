package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"slices"

	"github.com/abhinavxd/artemis/internal/attachment"
	"github.com/abhinavxd/artemis/internal/envelope"
	"github.com/abhinavxd/artemis/internal/image"
	"github.com/abhinavxd/artemis/internal/stringutil"
	umodels "github.com/abhinavxd/artemis/internal/user/models"
	"github.com/google/uuid"
	"github.com/valyala/fasthttp"
	"github.com/zerodha/fastglue"
)

const (
	thumbPrefix   = "thumb_"
	thumbnailSize = 150
)

func handleMediaUpload(r *fastglue.Request) error {
	var (
		app     = r.Context.(*App)
		cleanUp = false
	)

	form, err := r.RequestCtx.MultipartForm()
	if err != nil {
		app.lo.Error("error parsing form data.", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Error parsing data", nil, envelope.GeneralError)
	}

	files, ok := form.File["files"]
	if !ok || len(files) == 0 {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "File not found", nil, envelope.InputError)
	}

	fileHeader := files[0]
	file, err := fileHeader.Open()
	if err != nil {
		app.lo.Error("error reading uploaded file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Error reading file", nil, envelope.GeneralError)
	}
	defer file.Close()

	// Inline?
	var disposition = attachment.DispositionAttachment
	inline, ok := form.Value["inline"]
	if ok && len(inline) > 0 && inline[0] == "true" {
		disposition = attachment.DispositionInline
	}

	// Sanitize filename.
	srcFileName := stringutil.SanitizeFilename(fileHeader.Filename)
	srcContentType := fileHeader.Header.Get("Content-Type")
	srcFileSize := fileHeader.Size
	srcExt := strings.TrimPrefix(strings.ToLower(filepath.Ext(srcFileName)), ".")

	// Check file size
	if bytesToMegabytes(srcFileSize) > app.constant.MaxFileUploadSizeMB {
		app.lo.Error("error: uploaded file size is larger than max allowed", "size", bytesToMegabytes(srcFileSize), "max_allowed", app.constant.MaxFileUploadSizeMB)
		return r.SendErrorEnvelope(
			http.StatusRequestEntityTooLarge,
			fmt.Sprintf("File size is too large. Please upload file lesser than %f MB", app.constant.MaxFileUploadSizeMB),
			nil,
			envelope.GeneralError,
		)
	}

	if !slices.Contains(app.constant.AllowedUploadFileExtensions, "*") && !slices.Contains(app.constant.AllowedUploadFileExtensions, srcExt) {
		return r.SendErrorEnvelope(fasthttp.StatusBadRequest, "Unsupported file type", nil, envelope.InputError)
	}

	// Delete files on any error.
	var uuid = uuid.New()
	thumbName := thumbPrefix + uuid.String()
	defer func() {
		if cleanUp {
			app.media.Delete(uuid.String())
			app.media.Delete(thumbName)
		}
	}()

	// Generate and upload thumbnail if it's an image.
	if slices.Contains(image.Exts, srcExt) {
		file.Seek(0, 0)
		thumbFile, err := image.CreateThumb(thumbnailSize, file)
		if err != nil {
			app.lo.Error("error creating thumb image", "error", err)
			return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Error creating image thumbnail", nil, envelope.GeneralError)
		}
		thumbName, err = app.media.Upload(thumbName, srcContentType, thumbFile)
		if err != nil {
			app.lo.Error("error uploading thumbnail", "error", err)
			return sendErrorEnvelope(r, err)
		}
	}

	// Store image dimensions in the media meta.
	file.Seek(0, 0)
	width, height, err := image.GetDimensions(file)
	if err != nil {
		cleanUp = true
		app.lo.Error("error getting image dimensions", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Error uploading file", nil, envelope.GeneralError)
	}
	meta, _ := json.Marshal(map[string]interface{}{
		"width":  width,
		"height": height,
	})

	file.Seek(0, 0)
	_, err = app.media.Upload(uuid.String(), srcContentType, file)
	if err != nil {
		cleanUp = true
		app.lo.Error("error uploading file", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Error uploading file", nil, envelope.GeneralError)
	}

	// Insert in DB.
	media, err := app.media.Insert(srcFileName, srcContentType, "" /**content_id**/, "" /**model_type**/, disposition, uuid.String(), 0, int(srcFileSize), meta)
	if err != nil {
		cleanUp = true
		app.lo.Error("error inserting metadata into database", "error", err)
		return r.SendErrorEnvelope(fasthttp.StatusInternalServerError, "Error inserting media", nil, envelope.GeneralError)
	}
	return r.SendEnvelope(media)
}

// handleServeMedia serves uploaded media.
func handleServeMedia(r *fastglue.Request) error {
	var (
		app  = r.Context.(*App)
		user = r.RequestCtx.UserValue("user").(umodels.User)
		uuid = r.RequestCtx.UserValue("uuid").(string)
	)

	// Fetch media from DB.
	media, err := app.media.GetByUUID(strings.TrimPrefix(uuid, thumbPrefix))
	if err != nil {
		return sendErrorEnvelope(r, err)
	}

	// Check if the user has permission to access the linked model.
	allowed, err := app.authz.EnforceMediaAccess(user, media.Model.String)
	if err != nil  {
		app.lo.Error("error checking media permission", "error", err, "model", media.Model.String, "model_id", media.ModelID)
		return sendErrorEnvelope(r, err)
	}

	// For messages, check access to the conversation this message is part of.
	if media.Model.String == "messages" {
		conversation, err := app.conversation.GetConversationByMessageID(media.ModelID.Int)
		if err != nil {
			return sendErrorEnvelope(r, err)
		}
		allowed, err = app.authz.EnforceConversationAccess(user, conversation)
		if err != nil {
			return sendErrorEnvelope(r, err)
		}
	}

	if !allowed {
		return r.SendErrorEnvelope(http.StatusUnauthorized, "Permission denied", nil, envelope.PermissionError)
	}

	switch ko.String("upload.provider") {
	case "fs":
		fasthttp.ServeFile(r.RequestCtx, filepath.Join(ko.String("upload.fs.upload_path"), uuid))
	case "s3":
		r.RequestCtx.Redirect(app.media.GetURL(uuid), http.StatusFound)
	}
	return nil
}

func bytesToMegabytes(bytes int64) float64 {
	return float64(bytes) / 1024 / 1024
}
