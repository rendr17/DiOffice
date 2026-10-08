-- +goose Up
CREATE TABLE task_reference_images (
    organization_id uuid NOT NULL,
    task_id uuid NOT NULL,
    id uuid NOT NULL DEFAULT gen_random_uuid(),
    object_key text NOT NULL UNIQUE,
    file_name text NOT NULL
        CHECK (length(btrim(file_name)) BETWEEN 1 AND 124),
    content_type text NOT NULL
        CHECK (content_type IN ('image/png', 'image/jpeg')),
    size_bytes bigint NOT NULL
        CHECK (size_bytes BETWEEN 1 AND 8388608),
    sha256 text NOT NULL
        CHECK (sha256 ~ '^[a-f0-9]{64}$'),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (organization_id, task_id, id),
    CHECK (object_key = 'task-reference-images/' || id::text),
    FOREIGN KEY (organization_id, task_id)
        REFERENCES tasks (organization_id, id) ON DELETE CASCADE
);

CREATE INDEX task_reference_images_task_created_idx
    ON task_reference_images (organization_id, task_id, created_at, id);

-- +goose Down
DROP TABLE task_reference_images;
