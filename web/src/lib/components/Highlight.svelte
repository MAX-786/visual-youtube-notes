<script lang="ts">
  let {
    text = "",
    query = "",
    caseSensitive = false,
  }: { text?: string; query?: string; caseSensitive?: boolean } = $props();

  type Part = { text: string; match: boolean };

  function parts(value: string, needle: string, exactCase: boolean): Part[] {
    if (!needle) return [{ text: value, match: false }];

    const haystack = exactCase ? value : value.toLocaleLowerCase();
    const candidates = [needle, ...needle.split(/\s+/)]
      .map((part) => (exactCase ? part : part.toLocaleLowerCase()))
      .filter((part, index, list) => part && list.indexOf(part) === index)
      .sort((a, b) => b.length - a.length);
    if (!candidates.length) return [{ text: value, match: false }];

    const result: Part[] = [];
    let cursor = 0;
    while (cursor < value.length) {
      let index = -1;
      let length = 0;
      for (const candidate of candidates) {
        const found = haystack.indexOf(candidate, cursor);
        if (
          found !== -1 &&
          (index === -1 ||
            found < index ||
            (found === index && candidate.length > length))
        ) {
          index = found;
          length = candidate.length;
        }
      }
      if (index === -1) {
        result.push({ text: value.slice(cursor), match: false });
        break;
      }
      if (index > cursor) {
        result.push({ text: value.slice(cursor, index), match: false });
      }
      result.push({
        text: value.slice(index, index + length),
        match: true,
      });
      cursor = index + length;
    }
    return result.length ? result : [{ text: value, match: false }];
  }

  let rendered = $derived(parts(text, query.trim(), caseSensitive));
</script>

{#each rendered as part, index (index)}
  {#if part.match}<mark>{part.text}</mark>{:else}{part.text}{/if}
{/each}
