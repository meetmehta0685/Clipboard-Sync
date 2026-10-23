// Desktops expect PNG on the clipboard, so photos and other formats are
// re-encoded before sending (mirrors clipboard.NormalizeImage in Go).
export async function toPng(blob: Blob): Promise<Uint8Array> {
  if (blob.type === "image/png") return new Uint8Array(await blob.arrayBuffer());
  const bmp = await createImageBitmap(blob);
  const canvas = document.createElement("canvas");
  canvas.width = bmp.width;
  canvas.height = bmp.height;
  canvas.getContext("2d")!.drawImage(bmp, 0, 0);
  bmp.close();
  const png = await new Promise<Blob | null>((r) => canvas.toBlob(r, "image/png"));
  if (!png) throw new Error("Could not convert the image to PNG");
  return new Uint8Array(await png.arrayBuffer());
}
