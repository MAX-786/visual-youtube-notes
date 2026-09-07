<script lang="ts">
  import type { components } from "@visual-notes/api-client";
  import { byOrderThenId, type ComparisonDiagram } from "$lib/diagram";
  import { elementId } from "$lib/uuid";
  import Highlight from "./Highlight.svelte";

  type MutationOp = components["schemas"]["MutationOp"];

  let {
    diagram,
    editable = false,
    query = "",
    caseSensitive = false,
    emit,
  }: {
    diagram: ComparisonDiagram;
    editable?: boolean;
    query?: string;
    caseSensitive?: boolean;
    emit?: (ops: MutationOp[]) => void;
  } = $props();

  const columns = $derived([...diagram.columns].sort(byOrderThenId));
  const rows = $derived([...diagram.rows].sort(byOrderThenId));

  function label(id: string, input: HTMLInputElement): void {
    const value = input.value.trim();
    if (value)
      emit?.([{ op: "set_element_label", element_id: id, label: value }]);
  }
  function cell(
    rowId: string,
    columnId: string,
    input: HTMLTextAreaElement,
  ): void {
    const value = input.value.trim();
    if (value)
      emit?.([
        {
          op: "set_cell_text",
          row_id: rowId,
          column_id: columnId,
          text: value,
        },
      ]);
  }
  function remove(id: string, kind: "row" | "column"): void {
    if (
      !confirm(
        `Delete this ${kind}${kind === "column" ? " and its cells" : ""}?`,
      )
    )
      return;
    emit?.([{ op: "delete_element", element_id: id }]);
  }
  function add(kind: "rows" | "columns"): void {
    emit?.([
      {
        op: "insert_element",
        collection: kind,
        id: elementId(kind === "rows" ? "row" : "col"),
        label: kind === "rows" ? "New criterion" : "New option",
      },
    ]);
  }
  function enterToBlur(event: KeyboardEvent): void {
    if (event.key === "Enter") (event.currentTarget as HTMLInputElement).blur();
  }
</script>

{#if editable}
  <div class="table-tools" role="toolbar" aria-label="Edit comparison">
    <span>Edit the cells directly</span>
    <button
      class="small"
      type="button"
      disabled={columns.length >= 4}
      onclick={() => add("columns")}>Add option</button
    >
    <button
      class="small"
      type="button"
      disabled={rows.length >= 6}
      onclick={() => add("rows")}>Add criterion</button
    >
  </div>
{/if}

<div class:editable class="scroll">
  <table>
    <thead>
      <tr>
        <th scope="col">Criterion</th>
        {#each columns as column (column.id)}
          <th
            scope="col"
            class:claim={column.speaker_claim}
            data-element-id={column.id}
          >
            {#if editable}
              <div class="header-editor">
                <input
                  type="text"
                  value={column.label}
                  aria-label={`Column ${column.label}`}
                  onblur={(event) => label(column.id, event.currentTarget)}
                  onkeydown={enterToBlur}
                />
                <button
                  class="danger small"
                  type="button"
                  disabled={columns.length <= 2}
                  aria-label={`Delete ${column.label} column`}
                  onclick={() => remove(column.id, "column")}>×</button
                >
              </div>
            {:else}<Highlight
                text={column.label}
                {query}
                {caseSensitive}
              />{/if}
          </th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each rows as row (row.id)}
        <tr data-element-id={row.id}>
          <th scope="row" class:claim={row.speaker_claim}>
            {#if editable}
              <div class="header-editor">
                <input
                  type="text"
                  value={row.label}
                  aria-label={`Row ${row.label}`}
                  onblur={(event) => label(row.id, event.currentTarget)}
                  onkeydown={enterToBlur}
                />
                <button
                  class="danger small"
                  type="button"
                  disabled={rows.length <= 1}
                  aria-label={`Delete ${row.label} row`}
                  onclick={() => remove(row.id, "row")}>×</button
                >
              </div>
            {:else}<Highlight text={row.label} {query} {caseSensitive} />{/if}
          </th>
          {#each columns as column (column.id)}
            {@const value = row.cells[column.id]}
            <td class:claim={value?.speaker_claim}>
              {#if editable}
                <textarea
                  rows="3"
                  value={value?.text ?? ""}
                  aria-label={`${row.label}, ${column.label}`}
                  onblur={(event) =>
                    cell(row.id, column.id, event.currentTarget)}></textarea>
              {:else}<Highlight
                  text={value?.text ?? ""}
                  {query}
                  {caseSensitive}
                />{/if}
            </td>
          {/each}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .table-tools {
    display: flex;
    align-items: center;
    justify-content: flex-end;
    gap: 0.45rem;
    margin-bottom: 0.6rem;
  }
  .table-tools span {
    margin-right: auto;
    color: var(--muted);
    font-size: 0.8rem;
  }
  .scroll {
    overflow-x: auto;
    border: 1px solid var(--line);
    border-radius: var(--radius);
  }
  .scroll.editable {
    border-color: var(--accent);
    box-shadow: 0 0 0 3px var(--accent-soft);
  }
  table {
    border-collapse: separate;
    border-spacing: 0;
    width: 100%;
    min-width: 38rem;
    background: var(--surface);
  }
  th,
  td {
    padding: 0.8rem;
    border-right: 1px solid var(--line);
    border-bottom: 1px solid var(--line);
    text-align: left;
    vertical-align: top;
  }
  th:last-child,
  td:last-child {
    border-right: 0;
  }
  tbody tr:last-child th,
  tbody tr:last-child td {
    border-bottom: 0;
  }
  thead th {
    background: var(--surface-muted);
  }
  thead th:first-child {
    width: 18%;
  }
  tbody th {
    background: color-mix(in oklch, var(--surface-muted) 55%, var(--surface));
  }
  .claim {
    font-style: italic;
  }
  .header-editor {
    display: grid;
    grid-template-columns: 1fr auto;
    gap: 0.3rem;
  }
  .header-editor input,
  td textarea {
    min-width: 8rem;
    background: var(--surface);
    font-weight: inherit;
    font-style: inherit;
  }
  td textarea {
    min-height: 5rem;
    resize: vertical;
  }
  @media (max-width: 560px) {
    .table-tools {
      flex-wrap: wrap;
      justify-content: start;
    }
    .table-tools span {
      flex-basis: 100%;
    }
  }
</style>
