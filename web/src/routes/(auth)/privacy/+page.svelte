<svelte:head>
  <title>Privacy · Visual Notes</title>
  <meta
    name="description"
    content="How the Visual Notes browser extension and web app handle your data."
  />
</svelte:head>

<main class="policy-page" id="main-content">
  <header>
    <a class="brand" href="/">Visual Notes</a>
    <a href="/sign-in">Sign in</a>
  </header>

  <article>
    <p class="eyebrow">Privacy policy</p>
    <h1>Your notes stay yours.</h1>
    <p class="lede">
      Visual Notes saves a YouTube video only after you press <strong
        >Save to Visual Notes</strong
      >. It does not sell data, show advertising, or monitor unrelated browsing.
    </p>
    <p class="updated">Last updated 8 September 2026</p>

    <section>
      <h2>Information we handle</h2>
      <ul>
        <li>
          <strong>YouTube page information.</strong> On supported YouTube watch and
          Shorts pages, the extension reads the current video identifier so it can
          show the save control. The video URL is sent to Visual Notes only when you
          choose to save it.
        </li>
        <li>
          <strong>Notes and source material.</strong> The service retrieves public
          video metadata and captions, or temporary audio when captions are unavailable,
          to create the note and retained transcript.
        </li>
        <li>
          <strong>Authentication information.</strong> The extension stores its pairing
          tokens in Chrome's local extension storage. The service stores a browser
          label and session record so you can review and revoke access.
        </li>
        <li>
          <strong>Operational information.</strong> Limited security and reliability
          records, such as request time, IP address, failure reason, and processing
          usage, may be retained to operate and protect the service. Passwords, tokens,
          provider keys, and transcript bodies are not written to logs.
        </li>
      </ul>
    </section>

    <section>
      <h2>How information is used</h2>
      <p>
        Information is used only to pair your browser, save the video you
        selected, generate and search your visual notes, synchronize them across
        your devices, and keep the service secure and reliable.
      </p>
    </section>

    <section>
      <h2>Services involved</h2>
      <p>
        Visual Notes communicates with its own service at
        <a href="https://yt-track.mkhismkh.com">yt-track.mkhismkh.com</a> over HTTPS.
        It retrieves public source material from YouTube. Transcript or video content
        required for generation may be processed by OpenAI using the account and configuration
        selected by the Visual Notes owner. Those services handle data under their
        respective terms and privacy policies.
      </p>
      <p>
        We do not sell or transfer user data for advertising, credit decisions,
        or data-broker purposes. Humans do not read note content except with
        your specific permission for support, when required for security or law,
        or after aggregation and anonymization for internal operations.
      </p>
    </section>

    <section>
      <h2>Storage, control, and deletion</h2>
      <p>
        Pairing tokens remain in local extension storage until you unpair the
        browser, revoke its session, clear extension data, or uninstall the
        extension. Notes, transcripts, and related metadata remain in Visual
        Notes until you delete them or the service is shut down. You can delete
        notes in the app and revoke browser sessions under Settings.
      </p>
    </section>

    <section>
      <h2>Chrome Web Store Limited Use</h2>
      <p>
        Visual Notes' use and transfer of information received from Chrome APIs
        complies with the Chrome Web Store User Data Policy, including the
        Limited Use requirements.
      </p>
    </section>

    <section>
      <h2>Questions</h2>
      <p>
        For a privacy or deletion request, use the developer support contact
        shown on the Visual Notes Chrome Web Store listing.
      </p>
    </section>
  </article>
</main>

<style>
  .policy-page {
    width: min(100% - 2rem, 52rem);
    margin: 0 auto;
    padding-bottom: 5rem;
  }

  header {
    display: flex;
    min-height: var(--nav-height);
    align-items: center;
    justify-content: space-between;
    border-bottom: 1px solid var(--line);
  }

  header a {
    color: var(--muted);
    font-size: 0.85rem;
  }

  header .brand {
    color: var(--ink);
    font-size: 0.95rem;
    font-weight: 700;
    text-decoration: none;
  }

  article {
    padding-top: clamp(3rem, 8vw, 5.5rem);
  }

  .eyebrow {
    margin: 0 0 0.75rem;
    color: var(--accent);
    font-size: 0.75rem;
    font-weight: 700;
    letter-spacing: 0.1em;
    text-transform: uppercase;
  }

  h1 {
    max-width: 13ch;
    margin: 0;
    font-size: 3.8rem;
    letter-spacing: -0.055em;
    line-height: 0.98;
  }

  .lede {
    max-width: 62ch;
    margin: 1.5rem 0 0;
    color: var(--ink-soft);
    font-size: 1.05rem;
    line-height: 1.7;
  }

  .updated {
    margin: 1.25rem 0 0;
    color: var(--muted);
    font-size: 0.78rem;
  }

  section {
    padding-top: 2.25rem;
    margin-top: 2.25rem;
    border-top: 1px solid var(--line);
  }

  h2 {
    margin: 0 0 0.8rem;
    font-size: 1.12rem;
    letter-spacing: -0.02em;
  }

  section p,
  li {
    max-width: 70ch;
    color: var(--ink-soft);
    line-height: 1.72;
  }

  section p {
    margin: 0.75rem 0 0;
  }

  ul {
    display: grid;
    gap: 0.9rem;
    padding-left: 1.25rem;
  }

  strong {
    color: var(--ink);
  }

  @media (max-width: 36rem) {
    h1 {
      font-size: 2.7rem;
    }
  }
</style>
