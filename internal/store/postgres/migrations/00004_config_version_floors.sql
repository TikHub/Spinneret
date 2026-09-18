-- Version floors of deleted config items.
--
-- Nodes identify config content only by (namespace, group, key, version), so a
-- config item that is deleted and created again must never reuse a version
-- number of the deleted item: a node holding that version would treat the new
-- content as unchanged. Deleting an item records its last published version
-- here; the next version published under the same (namespace, group, key) is
-- max(current version, floor) + 1. Floors are kept after the item is created
-- again (a later deletion raises them) and are removed with their namespace.

-- +goose Up

CREATE TABLE config_version_floors (
    namespace_id text        NOT NULL REFERENCES namespaces (id) ON DELETE CASCADE,
    group_name   text        NOT NULL,
    key          text        NOT NULL,
    last_version integer     NOT NULL,
    updated_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (namespace_id, group_name, key),
    CONSTRAINT config_version_floors_last_version_check CHECK (last_version >= 0)
);

-- +goose Down

DROP TABLE IF EXISTS config_version_floors;
