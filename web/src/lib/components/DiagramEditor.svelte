<script lang="ts">
  // Edits diagram text and structure within V1's closed catalogue: labels of
  // nodes, events, columns, rows and labelled edges; comparison cells; and
  // deletion of elements (the server cascades). Nothing can be added.
  import { onDestroy } from "svelte";
  import { SvelteMap } from "svelte/reactivity";
  import type { components } from "@visual-notes/api-client";
  import { byOrderThenId, type Diagram } from "$lib/diagram";

  type MutationOp = components["schemas"]["MutationOp"];

  let {
    diagram,
    emit,
  }: { diagram: Diagram; emit: (ops: MutationOp[]) => void } = $props();

  const DEBOUNCE_MS = 2000;
  const timers = new SvelteMap<string, ReturnType<typeof setTimeout>>();
  const pending = new SvelteMap<string, MutationOp>();

  type Labelled = {
    id: string;
    label: string;
    what: string;
    deletable: boolean;
  };

  const labelled = $derived.by((): Labelled[] => {
    const d = diagram;
    if (d.type === "comparison") {
      return [
        ...[...d.columns].sort(byOrderThenId).map((c) => ({
          id: c.id,
          label: c.label,
          what: "column",
          deletable: d.columns.length > 2,
        })),
        ...[...d.rows].sort(byOrderThenId).map((r) => ({
          id: r.id,
          label: r.label,
          what: "row",
          deletable: d.rows.length > 1,
        })),
      ];
    }
    if (d.type === "timeline") {
      return [...d.events].sort(byOrderThenId).map((e) => ({
        id: e.id,
        label: e.label,
        what: "event",
        deletable: d.events.length > 1,
      }));
    }
    const nodes = d.nodes.map((n) => ({
      id: n.id,
      label: n.label,
      what: "kind" in n && n.kind === "root" ? "root" : "node",
      deletable:
        !("kind" in n && n.kind === "root") &&
        d.nodes.length > (d.type === "process_flow" ? 2 : 1),
    }));
    const edges = d.edges
      .filter((e) => e.label)
      .map((e) => ({
        id: e.id,
        label: e.label ?? "",
        what: "edge label",
        deletable: false,
      }));
    return [...nodes, ...edges];
  });

  const cells = $derived.by(() => {
    if (diagram.type !== "comparison") return [];
    const columns = [...diagram.columns].sort(byOrderThenId);
    return [...diagram.rows].sort(byOrderThenId).flatMap((row) =>
      columns.map((column) => ({
        key: `${row.id}:${column.id}`,
        row_id: row.id,
        column_id: column.id,
        text: row.cells[column.id]?.text ?? "",
        title: `${row.label} × ${column.label}`,
      })),
    );
  });

  function schedule(key: string, op: MutationOp) {
    pending.set(key, op);
    const existing = timers.get(key);
    if (existing) clearTimeout(existing);
    timers.set(
      key,
      setTimeout(() => flush(key), DEBOUNCE_MS),
    );
  }

  function flush(key: string) {
    const timer = timers.get(key);
    if (timer) clearTimeout(timer);
    timers.delete(key);
    const op = pending.get(key);
    pending.delete(key);
    if (op) emit([op]);
  }

  function labelInput(id: string, event: Event) {
    const label = (event.currentTarget as HTMLInputElement).value.trim();
    if (!label) return;
    schedule(`label:${id}`, { op: "set_element_label", element_id: id, label });
  }

  function cellInput(row_id: string, column_id: string, event: Event) {
    const text = (event.currentTarget as HTMLInputElement).value.trim();
    if (!text) return;
    schedule(`cell:${row_id}:${column_id}`, {
      op: "set_cell_text",
      row_id,
      column_id,
      text,
    });
  }

  function remove(item: Labelled) {
    const warning =
      item.what === "node"
        ? "Delete this element and its connections?"
        : item.what === "column"
          ? "Delete this column and its cells?"
          : `Delete this ${item.what}?`;
    if (!confirm(warning)) return;
    emit([{ op: "delete_element", element_id: item.id }]);
  }

  export function flushAll() {
    for (const key of [...timers.keys()]) flush(key);
  }
  onDestroy(flushAll);
</script>

<details class="card" open>
  <summary>Edit the diagram's text</summary>
  <div class="stack">
    {#each labelled as item (item.id)}
      <div class="row">
        <label class="field grow">
          <span>{item.what}</span>
          <input
            type="text"
            value={item.label}
            oninput={(e) => labelInput(item.id, e)}
            onblur={() => flush(`label:${item.id}`)}
          />
        </label>
        {#if item.deletable}
          <button type="button" class="danger" onclick={() => remove(item)}
            >Delete</button
          >
        {/if}
      </div>
    {/each}
    {#each cells as cell (cell.key)}
      <label class="field">
        <span>{cell.title}</span>
        <input
          type="text"
          value={cell.text}
          oninput={(e) => cellInput(cell.row_id, cell.column_id, e)}
          onblur={() => flush(`cell:${cell.row_id}:${cell.column_id}`)}
        />
      </label>
    {/each}
  </div>
</details>

<style>
  summary {
    cursor: pointer;
    font-weight: 600;
  }
  .row {
    align-items: flex-end;
  }
  .grow {
    flex: 1 1 12rem;
  }
  .field > span {
    text-transform: capitalize;
    font-weight: 500;
  }
</style>
