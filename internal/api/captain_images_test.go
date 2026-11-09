package api_test

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/shubham-tomar/launchcrew/internal/db/sqlc"
	"github.com/shubham-tomar/launchcrew/internal/session"
)

// imageForm is a multipart body with one field, image, holding data.
func imageForm(t *testing.T, name string, data []byte) (body, contentType string) {
	t.Helper()
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	fw, err := w.CreateFormFile("image", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.String(), w.FormDataContentType()
}

func tinyPNG(t *testing.T, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 3, 3))
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func (e *captainEnv) upload(t *testing.T, c *http.Cookie, data []byte) (int, map[string]any) {
	t.Helper()
	body, ct := imageForm(t, "photo.png", data)
	rr := e.do("POST", "/api/captain/launches/"+e.launch.ID.String()+"/images", body, map[string]string{"Content-Type": ct}, c)
	return rr.Code, decode(t, rr)
}

func (e *captainEnv) detailJSON(t *testing.T, c *http.Cookie) map[string]any {
	t.Helper()
	rr := e.do("GET", "/api/captain/launches/"+e.launch.ID.String(), "", nil, c)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail: %d %s", rr.Code, rr.Body)
	}
	return decode(t, rr)
}

func TestCaptainUploadsAnImage(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})

	code, got := e.upload(t, mine, tinyPNG(t, color.RGBA{255, 0, 0, 255}))
	if code != http.StatusCreated {
		t.Fatalf("upload: %d %v", code, got)
	}
	url, _ := got["url"].(string)
	if !strings.HasPrefix(url, "https://cdn.example/uploads/") || !strings.HasSuffix(url, ".png") {
		t.Errorf("url = %q", url)
	}
	if _, err := uuid.Parse(got["id"].(string)); err != nil {
		t.Errorf("id = %v", got["id"])
	}
	d := e.detailJSON(t, mine)
	imgs, _ := d["images"].([]any)
	if len(imgs) != 1 || imgs[0].(map[string]any)["url"] != url {
		t.Errorf("detail images = %v, want the upload", d["images"])
	}
	if d["includeCard"] != true {
		t.Errorf("includeCard = %v, want true by default", d["includeCard"])
	}
}

func TestCaptainUploadTooLarge(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	big := bytes.Repeat([]byte{0x89}, 11<<20)
	code, got := e.upload(t, mine, big)
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("11 MB upload: %d %v", code, got)
	}
	if got["message"] != "Each image can be up to 10 MB." {
		t.Errorf("message = %v", got["message"])
	}
}

func TestCaptainUploadNotAnImage(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	code, got := e.upload(t, mine, []byte("ftypheic renamed to .jpg"))
	if code != http.StatusBadRequest {
		t.Fatalf("heic: %d %v", code, got)
	}
	if got["message"] != "Use a JPEG, PNG, GIF or WebP image." {
		t.Errorf("message = %v", got["message"])
	}
}

func TestCaptainImageRoutesNeedTheOwner(t *testing.T) {
	e := setupCaptain(t)
	theirs := e.cookie(t, session.Session{UserID: e.other.ID})
	base := "/api/captain/launches/" + e.launch.ID.String()
	body, ct := imageForm(t, "p.png", tinyPNG(t, color.White))
	if rr := e.do("POST", base+"/images", body, map[string]string{"Content-Type": ct}, theirs); rr.Code != http.StatusNotFound {
		t.Errorf("POST images by another user: %d", rr.Code)
	}
	if rr := e.do("DELETE", base+"/images/"+uuid.NewString(), "", nil, theirs); rr.Code != http.StatusNotFound {
		t.Errorf("DELETE image by another user: %d", rr.Code)
	}
	if rr := e.do("PUT", base+"/images/order", `{"ids":[]}`, nil, theirs); rr.Code != http.StatusNotFound {
		t.Errorf("PUT order by another user: %d", rr.Code)
	}
}

func TestCaptainImagesLockedOnceOut(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	code, first := e.upload(t, mine, tinyPNG(t, color.Black))
	if code != http.StatusCreated {
		t.Fatalf("upload: %d %v", code, first)
	}
	if err := e.q.UpdateLaunchStatus(e.ctx, sqlc.UpdateLaunchStatusParams{ID: e.launch.ID, Status: "launched"}); err != nil {
		t.Fatal(err)
	}
	base := "/api/captain/launches/" + e.launch.ID.String()
	code, got := e.upload(t, mine, tinyPNG(t, color.White))
	if code != http.StatusConflict || got["message"] != "Images are locked once the launch is out." {
		t.Errorf("upload after launch: %d %v", code, got)
	}
	rr := e.do("DELETE", base+"/images/"+first["id"].(string), "", nil, mine)
	if rr.Code != http.StatusConflict {
		t.Errorf("delete after launch: %d %s", rr.Code, rr.Body)
	}
}

func TestCaptainRemovesAndOrdersImages(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	base := "/api/captain/launches/" + e.launch.ID.String()
	_, a := e.upload(t, mine, tinyPNG(t, color.RGBA{1, 0, 0, 255}))
	_, b := e.upload(t, mine, tinyPNG(t, color.RGBA{0, 1, 0, 255}))
	_, c := e.upload(t, mine, tinyPNG(t, color.RGBA{0, 0, 1, 255}))

	rr := e.do("PUT", base+"/images/order", `{"ids":["`+a["id"].(string)+`","`+uuid.NewString()+`","`+c["id"].(string)+`"]}`, nil, mine)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("order with a foreign id: %d %s", rr.Code, rr.Body)
	}
	order, _ := json.Marshal(map[string]any{"ids": []string{c["id"].(string), a["id"].(string), b["id"].(string)}})
	if rr := e.do("PUT", base+"/images/order", string(order), nil, mine); rr.Code != http.StatusNoContent {
		t.Fatalf("order: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("DELETE", base+"/images/"+a["id"].(string), "", nil, mine); rr.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rr.Code, rr.Body)
	}
	if rr := e.do("DELETE", base+"/images/"+a["id"].(string), "", nil, mine); rr.Code != http.StatusNotFound {
		t.Errorf("delete again: %d", rr.Code)
	}
	imgs := e.detailJSON(t, mine)["images"].([]any)
	if len(imgs) != 2 || imgs[0].(map[string]any)["id"] != c["id"] || imgs[1].(map[string]any)["id"] != b["id"] {
		t.Errorf("images = %v, want c then b", imgs)
	}
}

func TestCaptainPatchIncludeCard(t *testing.T) {
	e := setupCaptain(t)
	mine := e.cookie(t, session.Session{UserID: e.owner.ID})
	base := "/api/captain/launches/" + e.launch.ID.String()
	rr := e.do("PATCH", base, `{"includeCard":false}`, nil, mine)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body)
	}
	if decode(t, rr)["includeCard"] != false {
		t.Errorf("patch response includeCard = %v", decode(t, rr)["includeCard"])
	}
	if d := e.detailJSON(t, mine); d["includeCard"] != false {
		t.Errorf("detail includeCard = %v, want false", d["includeCard"])
	}
	if rr := e.do("PATCH", base, `{"title":"Renamed"}`, nil, mine); decode(t, rr)["includeCard"] != false {
		t.Errorf("an unrelated patch flipped includeCard back: %s", rr.Body)
	}
}
