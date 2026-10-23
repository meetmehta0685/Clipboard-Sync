// Desktops expect PNG on the clipboard, so photos and other formats are
// re-encoded before sending (mirrors clipboard.NormalizeImage in Go).
//
// A 12 MP phone photo is a few MB as JPEG but can be 20-40 MB as PNG, which
// would exceed the clip size limit and be dropped by the receivers. So the
// image is scaled down until its PNG fits under maxBytes.
const MAX_SIDE = 4096; // also keeps us under mobile canvas size limits

export async function toPng(blob: Blob, maxBytes: number): Promise<Uint8Array> {
  if (blob.type === "image/png" && blob.size <= maxBytes) return new Uint8Array(await blob.arrayBuffer());
  const bmp = await createImageBitmap(blob);
  try {
    let scale = Math.min(1, MAX_SIDE / Math.max(bmp.width, bmp.height));
    for (let attempt = 0; attempt < 6; attempt++) {
      const png = await render(bmp, scale);
      if (png.size <= maxBytes) return new Uint8Array(await png.arrayBuffer());
      // PNG size grows roughly with pixel count; aim a little under the limit.
      scale *= Math.sqrt(maxBytes / png.size) * 0.9;
    }
    throw new Error("This image is too large to send, even after shrinking it.");
  } finally {
    bmp.close();
  }
}

async function render(bmp: ImageBitmap, scale: number): Promise<Blob> {
  const canvas = document.createElement("canvas");
  canvas.width = Math.max(1, Math.round(bmp.width * scale));
  canvas.height = Math.max(1, Math.round(bmp.height * scale));
  const ctx = canvas.getContext("2d")!;
  ctx.imageSmoothingQuality = "high";
  ctx.drawImage(bmp, 0, 0, canvas.width, canvas.height);
  const png = await new Promise<Blob | null>((r) => canvas.toBlob(r, "image/png"));
  if (!png) throw new Error("Could not convert the image to PNG");
  return png;
}
