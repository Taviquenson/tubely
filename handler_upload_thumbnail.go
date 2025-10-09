package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	fmt.Println("uploading thumbnail for video", videoID, "by user", userID)

	// Upload
	const maxMemory = 10 << 20 // Set to 10MB
	r.ParseMultipartForm(maxMemory)

	// "thumbnail" should match the HTML form input name
	mFile, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer mFile.Close()

	// Get the video's metadata
	dbVideo, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Unable to find video metadata", err)
		return
	}
	// Authorize user as video owner
	if dbVideo.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized: user is not video owner", err)
		return
	}

	// Get the media type from the form file's Content-Type header
	mediaType := header.Header.Get("Content-Type")
	// Determine the file extension from mediaType
	parts := strings.Split(mediaType, "/")
	fileExt := parts[1]
	// Build file path: /assets/<videoID>.<file_extension>
	fileName := videoIDString + "." + fileExt
	filePath := filepath.Join(cfg.assetsRoot, fileName)

	// Create the new file in the system
	file, err := os.Create(filePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't create file in system storage", err)
		return
	}
	// Copy contents from multipart file to system file
	_, err = io.Copy(file, mFile)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Couldn't copy contents to system file", err)
		return
	}

	thumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s.%s", cfg.port, videoIDString, fileExt)
	dbVideo.ThumbnailURL = &thumbnailURL
	// in main.go we have a file server that serves files from the /assets directory

	err = cfg.db.UpdateVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video metadata", err)
		return
	}

	respondWithJSON(w, http.StatusOK, dbVideo)
}
