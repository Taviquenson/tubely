package main

import (
	"fmt"
	"io"
	"net/http"

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

	// TODO: implement the upload here
	const maxMemory = 10 << 20 // Set to 10MB
	r.ParseMultipartForm(maxMemory)

	// "thumbnail" should match the HTML form input name
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse form file", err)
		return
	}
	defer file.Close()

	// Get the media type from the form file's Content-Type header
	mediaType := header.Header.Get("Content-Type")

	// Read all the image data into a byte slice
	imgData, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to read parsed file", err)
		return
	}

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

	tn := thumbnail{
		data:      imgData,
		mediaType: mediaType,
	}
	// Add the thumbnail to the global thumbnails map
	videoThumbnails[videoID] = tn

	thumbnailURL := fmt.Sprintf("http://localhost:%v/api/thumbnails/%v", cfg.port, videoIDString)
	dbVideo.ThumbnailURL = &thumbnailURL
	// This works because the /api/thumbnails/{videoID} endpoint serves thumbnails from that global map

	err = cfg.db.UpdateVideo(dbVideo)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to update video metadata", err)
		return
	}

	respondWithJSON(w, http.StatusOK, dbVideo)
}
