import { test, expect, Page } from "@playwright/test";

// media-upload.js behaviour on the /send Media tab, in a real browser.
//
// WHAT THIS CAN AND CANNOT PROVE. /send renders its tabs only for a
// CONNECTED session and this harness has none (see media.spec.ts's header),
// so the real page is unreachable. Instead this serves a FIXTURE page at
// /__media_fixture (intercepted in the browser; the app never sees it) that
// links the app's REAL /static css and media-upload.js and carries a copy of
// the Media tab's file/kind/caption/ptt markup, with the same per-kind
// data-accept values the Go side emits (mediaKindAccept, api_media.go). The
// JS and CSS are therefore the shipped ones; the markup is a copy, whose
// shape internal/handler/media_send_ui_test.go pins against send.html.
// Nothing here sends a message or proves a WhatsApp delivery.

const FIXTURE = `<!doctype html><html><head>
<link rel="stylesheet" href="/static/css/base.css">
<link rel="stylesheet" href="/static/css/components.css">
<link rel="stylesheet" href="/static/css/forms.css">
<link rel="stylesheet" href="/static/css/media.css">
</head><body><form id="media-send-form">
<select name="kind" id="media-kind-select" class="select">
<option value="image" data-max-bytes="1000" data-accept="image/jpeg,image/png,image/webp">Image</option>
<option value="video" data-max-bytes="100000" data-accept="video/mp4,video/webm,video/3gpp,video/quicktime">Video</option>
<option value="audio" data-max-bytes="100000" data-accept="audio/ogg,audio/mpeg,audio/mp4">Audio</option>
<option value="document" data-max-bytes="100000" data-accept="">Document</option>
</select>
<div class="media-dropzone" data-media-dropzone data-max-bytes="100000">
<input type="file" name="file" id="media-file-input">
<p class="media-drop-hint" id="media-drop-hint">or drop a file here</p>
<div class="media-preview" id="media-preview" hidden>
<img class="media-preview-img" id="media-preview-img" alt="" hidden>
<span class="media-badge" id="media-preview-badge" hidden></span>
<p class="media-file-chip" id="media-file-chip" hidden></p>
<button type="button" class="btn btn-outline media-clear" id="media-clear">Remove file</button>
</div>
<p class="media-kind-notice" id="media-kind-notice" role="status" hidden></p>
<div class="alert alert-error media-size-error" id="media-size-error" hidden></div>
<div class="media-progress" id="media-progress" hidden><div class="media-progress-bar" id="media-progress-bar"></div></div>
</div>
<label data-media-caption><textarea name="caption"></textarea></label>
<label data-media-ptt><input type="checkbox" name="ptt" value="true"> voice</label>
<button type="submit">Send</button>
</form><script src="/static/js/media-upload.js"></script></body></html>`;

// 1x1 transparent PNG.
const PNG = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==",
  "base64",
);
const PDF = Buffer.from("%PDF-1.4\n%%EOF\n");

async function open(page: Page) {
  await page.route("**/__media_fixture", (r) =>
    r.fulfill({ contentType: "text/html", body: FIXTURE }),
  );
  await page.goto("/__media_fixture");
}

const file = (name: string, mimeType: string, buffer: Buffer) => ({ name, mimeType, buffer });

test("the picker's accept list follows the Kind and document is unrestricted", async ({ page }) => {
  await open(page);
  const input = page.locator("#media-file-input");
  await expect(input).toHaveAttribute("accept", "image/jpeg,image/png,image/webp");
  await page.selectOption("#media-kind-select", "audio");
  await expect(input).toHaveAttribute("accept", "audio/ogg,audio/mpeg,audio/mp4");
  await page.selectOption("#media-kind-select", "document");
  await expect(input).not.toHaveAttribute("accept", /.+/);
});

test("an image shows a thumbnail; Remove file clears it", async ({ page }) => {
  await open(page);
  await page.setInputFiles("#media-file-input", file("pic.png", "image/png", PNG));
  await expect(page.locator("#media-preview-img")).toBeVisible();
  await expect(page.locator("#media-preview-img")).toHaveAttribute("src", /^blob:/);
  await expect(page.locator("#media-preview-badge")).toBeHidden();
  await page.click("#media-clear");
  await expect(page.locator("#media-preview")).toBeHidden();
  expect(await page.locator("#media-file-input").evaluate((e: HTMLInputElement) => e.files!.length)).toBe(0);
});

test("a PDF chosen under Image switches Kind to Document and shows a PDF badge", async ({ page }) => {
  await open(page);
  await page.setInputFiles("#media-file-input", file("doc.pdf", "application/pdf", PDF));
  await expect(page.locator("#media-kind-select")).toHaveValue("document");
  await expect(page.locator("#media-preview-badge")).toHaveText("PDF");
  await expect(page.locator("#media-kind-notice")).toContainText("Document");
  await expect(page.locator("#media-size-error")).toBeHidden();
  await expect(page.locator("button[type=submit]")).toBeEnabled();
});

test("changing Kind away from a PDF's fits blocks submit instead of silently switching", async ({ page }) => {
  await open(page);
  await page.setInputFiles("#media-file-input", file("doc.pdf", "application/pdf", PDF));
  await page.selectOption("#media-kind-select", "image");
  await expect(page.locator("#media-kind-select")).toHaveValue("image");
  await expect(page.locator("#media-size-error")).toContainText("cannot be sent as Image");
  await expect(page.locator("button[type=submit]")).toBeDisabled();
});

test("an oversize image is blocked with the limit message", async ({ page }) => {
  await open(page);
  await page.setInputFiles("#media-file-input", file("big.png", "image/png", Buffer.concat([PNG, Buffer.alloc(2000)])));
  await expect(page.locator("#media-size-error")).toContainText("larger than");
  await expect(page.locator("button[type=submit]")).toBeDisabled();
});

test("dropping a file on the dropzone selects it", async ({ page }) => {
  await open(page);
  await page.evaluate((b64) => {
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], "dropped.png", { type: "image/png" }));
    const zone = document.querySelector("[data-media-dropzone]")!;
    zone.dispatchEvent(new DragEvent("dragover", { dataTransfer: dt, bubbles: true, cancelable: true }));
    if (!zone.classList.contains("is-dragover")) throw new Error("no drag-over state");
    zone.dispatchEvent(new DragEvent("drop", { dataTransfer: dt, bubbles: true, cancelable: true }));
  }, PNG.toString("base64"));
  await expect(page.locator("#media-file-chip")).toContainText("dropped.png");
  await expect(page.locator("#media-preview-img")).toBeVisible();
  await expect(page.locator("[data-media-dropzone]")).not.toHaveClass(/is-dragover/);
});

test("caption shows for image and hides for audio, where the voice note shows instead", async ({ page }) => {
  await open(page);
  await expect(page.locator("[data-media-caption]")).toBeVisible();
  await expect(page.locator("[data-media-ptt]")).toBeHidden();
  await page.selectOption("#media-kind-select", "audio");
  await expect(page.locator("[data-media-caption]")).toBeHidden();
  await expect(page.locator("[data-media-ptt]")).toBeVisible();
});

test("progress resets on a failed request and the form clears after a successful send", async ({ page }) => {
  await open(page);
  await page.setInputFiles("#media-file-input", file("pic.png", "image/png", PNG));
  await page.fill("textarea[name=caption]", "hello");
  const form = page.locator("#media-send-form");
  const show = () => page.evaluate(() => {
    document.getElementById("media-progress")!.hidden = false;
    document.getElementById("media-progress-bar")!.style.width = "60%";
  });

  await show();
  await form.evaluate((f) => f.dispatchEvent(new CustomEvent("htmx:responseError", { bubbles: true })));
  await expect(page.locator("#media-progress")).toBeHidden();
  await expect(page.locator("#media-progress-bar")).toHaveAttribute("style", /width:\s*0%/);
  await expect(page.locator("#media-preview")).toBeVisible(); // a failure keeps the file

  await show();
  await form.evaluate((f) => f.dispatchEvent(new CustomEvent("htmx:afterRequest", {
    bubbles: true,
    detail: { elt: f, successful: true, xhr: { responseText: '<div class="alert alert-success">Sent</div>' } },
  })));
  await expect(page.locator("#media-preview")).toBeHidden();
  await expect(page.locator("textarea[name=caption]")).toHaveValue("");
  await expect(page.locator("#media-progress")).toBeHidden();
});
