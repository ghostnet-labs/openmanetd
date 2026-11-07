CREATE TABLE IF NOT EXISTS mesh_nodes (
  mac_addr       text PRIMARY KEY NOT NULL,
  hostname       text NOT NULL,
  ip_addr        text NOT NULL,
  latitude       real,
  longitude      real,
  altitude       real,
  uci_dhcp_start integer,
  uci_dhcp_limit integer,
  created_at     timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP,
  updated_at     timestamp NOT NULL DEFAULT CURRENT_TIMESTAMP
);
-- coverage_cells is this node's own coverage map: one row per grid cell
-- it has stood in, with how well it heard the mesh there. Bounded by
-- the coverage recorder (tak.coverage.maxCells); oldest cells go first.
CREATE TABLE IF NOT EXISTS coverage_cells (
  cell_m          integer NOT NULL,
  cell_row        integer NOT NULL,
  cell_col        integer NOT NULL,
  samples         integer NOT NULL,
  no_link_samples integer NOT NULL,
  signal_dbm      real NOT NULL,
  max_links       integer NOT NULL,
  first_seen      timestamp NOT NULL,
  last_seen       timestamp NOT NULL,
  PRIMARY KEY (cell_m, cell_row, cell_col)
);
