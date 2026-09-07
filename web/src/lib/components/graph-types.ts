export type GraphNodeData = {
  label: string;
  root: boolean;
  claim: boolean;
  editable: boolean;
  addable: boolean;
  deletable: boolean;
  connectable: boolean;
  addLabel: string;
  hiddenCount: number;
  expanded: boolean;
  query: string;
  caseSensitive: boolean;
  onCommit: (id: string, label: string) => void;
  onDelete: (id: string) => void;
  onAdd: (id: string) => void;
  onExpand: (id: string) => void;
};

export type GraphEdgeData = {
  editable: boolean;
  deletable: boolean;
  query: string;
  caseSensitive: boolean;
  onCommit: (id: string, label: string) => void;
  onDelete: (id: string) => void;
};
