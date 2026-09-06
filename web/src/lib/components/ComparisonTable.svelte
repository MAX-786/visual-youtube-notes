<script lang="ts">
  // A comparison matrix is a table: columns and rows in their declared
  // order, one cell per column, selectable and searchable as plain text.
  import { byOrderThenId, type ComparisonDiagram } from "$lib/diagram";

  let { diagram }: { diagram: ComparisonDiagram } = $props();

  const columns = $derived([...diagram.columns].sort(byOrderThenId));
  const rows = $derived([...diagram.rows].sort(byOrderThenId));
</script>

<div class="scroll">
  <table>
    <thead>
      <tr>
        <th scope="col">Criterion</th>
        {#each columns as column (column.id)}
          <th scope="col" class:claim={column.speaker_claim}>{column.label}</th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each rows as row (row.id)}
        <tr data-element-id={row.id}>
          <th scope="row" class:claim={row.speaker_claim}>{row.label}</th>
          {#each columns as column (column.id)}
            {@const cell = row.cells[column.id]}
            <td class:claim={cell?.speaker_claim}>{cell?.text ?? ""}</td>
          {/each}
        </tr>
      {/each}
    </tbody>
  </table>
</div>

<style>
  .scroll {
    overflow-x: auto;
  }
  table {
    border-collapse: collapse;
    width: 100%;
    min-width: 32rem;
    background: var(--surface);
  }
  th,
  td {
    border: 1px solid var(--line);
    padding: 0.5rem 0.75rem;
    text-align: left;
    vertical-align: top;
  }
  thead th {
    background: var(--bg);
  }
  .claim {
    font-style: italic;
  }
</style>
