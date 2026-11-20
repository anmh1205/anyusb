package esp

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http/httptest"
	"testing"
)

func uploadRequest(t *testing.T, manifest string, files map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	multipart := multipart.NewWriter(&body)
	if err := multipart.WriteField("manifest", manifest); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		part, err := multipart.CreateFormFile("files", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fmt.Fprint(part, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := multipart.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("POST", "https://anyusb/esp", &body)
	request.Header.Set("Content-Type", multipart.FormDataContentType())
	response := httptest.NewRecorder()
	_, filesOut, err := readUpload(response, request, t.TempDir())
	if err != nil {
		fmt.Fprint(response, err)
		return response
	}
	for _, file := range filesOut {
		fmt.Fprintf(response, "%x %s %d\n", file.Offset, file.Name, file.Size)
	}
	return response
}

func TestManifestUpload(t *testing.T) {
	files := map[string]string{"boot/boot.bin": "one", "app.bin": "two"}
	good := uploadRequest(t, `{"flash_files":{"0x0":"boot/boot.bin","0x10000":"app.bin"}}`, files)
	if good.Body.String() != "0 boot/boot.bin 3\n10000 app.bin 3\n" {
		t.Fatalf("valid upload: %s", good.Body)
	}
	for _, test := range []struct{ manifest, want string }{
		{`{"flash_files":{"0x0":"boot/boot.bin","0x2":"app.bin"}}`, "overlapping"},
		{`{"flash_files":{"0x0":"../../../file"}}`, "invalid offset or missing file"},
		{`{"flash_files":{"0x0":"missing.bin"}}`, "missing file"},
	} {
		response := uploadRequest(t, test.manifest, files)
		if !bytes.Contains(response.Body.Bytes(), []byte(test.want)) {
			t.Fatalf("invalid upload: want %q, got %s", test.want, response.Body)
		}
	}
}
