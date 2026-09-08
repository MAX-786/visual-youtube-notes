# Chrome Web Store submission

## 1. Upload package

Run:

```bash
pnpm --filter @visual-notes/extension build
```

Upload `extension/release/visual-notes-1.0.0.zip`. Do not upload `extension/dist`, the repository, or
an archive that wraps `dist` in another folder.

After the first successful upload, copy the Item ID shown by the Developer Dashboard. Set
`CHROME_EXTENSION_ID` in the deployed `deploy/.env` and recreate the API before testing pairing.

## 2. Store listing

**Name**

Visual Notes

**Summary**

Save the YouTube video you are watching as a transcript-grounded visual note.

**Category**

Productivity

**Detailed description**

> Turn the YouTube video you are watching into a visual note you can search and revisit.
>
> Visual Notes adds a focused save control to YouTube watch and Shorts pages. When you choose to
> save, it sends that video's public URL to your Visual Notes account for transcript-grounded note
> generation.
>
> • Save from the YouTube page without copying a link  
> • Avoid duplicate notes automatically  
> • Undo an accidental save for five seconds  
> • Open the finished note in the Visual Notes web app  
> • Pair and revoke each browser independently
>
> A Visual Notes account on yt-track.mkhismkh.com is required. The extension does not show ads,
> track unrelated browsing, or sell user data.

**Website**

`https://yt-track.mkhismkh.com`

**Privacy policy**

`https://yt-track.mkhismkh.com/privacy`

Use the padded 128 × 128 icon from `extension/icons/icon-128.png` and the prepared 440 × 280 tile at
`extension/store-assets/small-promo-tile.png`. Provide at least one current 1280 × 800 screenshot
showing the in-page save control and resulting confirmation. A second screenshot showing the paired
popup is recommended. Screenshots must be full bleed with square corners.

## 3. Privacy practices

**Single purpose**

Save the YouTube video explicitly selected by the user to their Visual Notes account for visual-note
generation and provide a link to the saved note.

**Permission justifications**

- `storage`: Stores browser-pairing tokens and the browser label locally so the user stays connected
  to their Visual Notes account. It does not use synchronized storage.
- `https://yt-track.mkhismkh.com/*`: Sends pairing, save, undo, and sign-out requests only to the
  Visual Notes service. Authentication credentials are sent only to this HTTPS origin.
- YouTube content-script matches: Adds the save control only to YouTube watch and Shorts pages and
  validates the current video identifier. It does not request access to other YouTube pages or other
  websites.

**Remote code**

Select **No, I am not using remote code**. All executable JavaScript is bundled inside the upload.
The HTTPS API processes user-requested saves but does not send executable code to the extension.

**Data disclosures**

Declare:

- Web history: the current YouTube video URL is transmitted only after the user presses Save.
- Authentication information: pairing tokens are stored in local extension storage and sent only
  to the Visual Notes HTTPS API.

The extension does not collect personally identifiable information, financial information, health
information, personal communications, location, or unrelated browsing activity. Certify that data
is not sold, used for advertising or credit decisions, or used outside the extension's single
purpose. Keep these answers consistent with the public privacy policy.

## 4. Reviewer instructions

Because pairing requires the restricted Owner account, provide a temporary reviewer account in the
Dashboard's **Test instructions** tab. Do not put credentials in the listing description or source
package.

Suggested instructions:

> 1. Install the extension and open its toolbar popup.
> 2. Select “Pair this browser.”
> 3. Sign in to Visual Notes using the temporary credentials supplied in the protected credentials
>    field, then approve the browser.
> 4. Open any public YouTube watch page.
> 5. Select “Save to Visual Notes” near the video actions.
> 6. The confirmation appears on YouTube. Select “Open note” on a duplicate, or open Visual Notes to
>    see the newly submitted note.
> 7. No payment is required.

Revoke and remove the temporary reviewer account after the review is complete.

## 5. Submit safely

1. Verify the developer contact email and enable two-step verification.
2. Complete Store listing, Privacy practices, Distribution, and Test instructions.
3. Prefer **Private** or **Unlisted** for the first reviewed release unless public discovery is
   intentional. All visibility levels receive the same policy review.
4. Choose deferred publishing when submitting for review. This lets you confirm the backend Item ID
   and final production behavior before making the approved release available.
5. Every later upload must increase `version` in `manifest.json`.
