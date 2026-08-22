-- +goose Up
-- True 2-D rack placement on a floor plan. Rooms describe a raised-
-- floor tile grid (grid_cols x grid_rows); racks occupy one tile at
-- (grid_x, grid_y), 0-indexed from the room's top-left corner, facing
-- per grid_rotation (degrees clockwise; 0 = front faces down/south on
-- the rendered plan). NULL grid_x/grid_y = not placed on the plan yet.
--
-- Cell exclusivity is enforced in the UI, not the schema: a rack's
-- room is 2 FK hops away (rack -> row -> room), so a UNIQUE across
-- (room, x, y) would need a denormalized room_id we don't otherwise
-- want. Two racks on one tile renders as a stack, not corruption.

ALTER TABLE racks
    ADD COLUMN IF NOT EXISTS grid_x INT,
    ADD COLUMN IF NOT EXISTS grid_y INT,
    ADD COLUMN IF NOT EXISTS grid_rotation SMALLINT NOT NULL DEFAULT 0;

ALTER TABLE racks
    ADD CONSTRAINT ck_racks_grid_x_nonneg CHECK (grid_x IS NULL OR grid_x >= 0),
    ADD CONSTRAINT ck_racks_grid_y_nonneg CHECK (grid_y IS NULL OR grid_y >= 0),
    ADD CONSTRAINT ck_racks_grid_rotation CHECK (grid_rotation IN (0, 90, 180, 270));

ALTER TABLE rooms
    ADD COLUMN IF NOT EXISTS grid_cols INT,
    ADD COLUMN IF NOT EXISTS grid_rows INT;

ALTER TABLE rooms
    ADD CONSTRAINT ck_rooms_grid_cols_pos CHECK (grid_cols IS NULL OR grid_cols > 0),
    ADD CONSTRAINT ck_rooms_grid_rows_pos CHECK (grid_rows IS NULL OR grid_rows > 0);

-- +goose Down
ALTER TABLE rooms
    DROP CONSTRAINT IF EXISTS ck_rooms_grid_cols_pos,
    DROP CONSTRAINT IF EXISTS ck_rooms_grid_rows_pos;
ALTER TABLE rooms
    DROP COLUMN IF EXISTS grid_cols,
    DROP COLUMN IF EXISTS grid_rows;
ALTER TABLE racks
    DROP CONSTRAINT IF EXISTS ck_racks_grid_x_nonneg,
    DROP CONSTRAINT IF EXISTS ck_racks_grid_y_nonneg,
    DROP CONSTRAINT IF EXISTS ck_racks_grid_rotation;
ALTER TABLE racks
    DROP COLUMN IF EXISTS grid_x,
    DROP COLUMN IF EXISTS grid_y,
    DROP COLUMN IF EXISTS grid_rotation;
