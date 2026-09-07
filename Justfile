set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

image := env_var_or_default("IMAGE", "costume-tree:dev")
container := env_var_or_default("CONTAINER", "costume-tree")
volume := env_var_or_default("VOLUME", "costume-tree-data")
photo_volume := env_var_or_default("PHOTO_VOLUME", "costume-tree-photos")
port := env_var_or_default("PORT", "8080")
image_repository := env_var_or_default("IMAGE_REPOSITORY", "ghcr.io/bkroeze/costume-tree")
image_tag := env_var_or_default("IMAGE_TAG", `git rev-parse --short HEAD`)



# Show the available project commands.
default:
    @just --list

# Run the Go unit and integration tests.
test:
    go test ./...

# Run static analysis.
vet:
    go vet ./...

# Run the local verification gate.
check: test vet

# Format tracked Go sources.
fmt:
    gofmt -w $$(git ls-files '*.go')

# Start the application from the host Go toolchain.
run:
    go run ./cmd/costume-tree

# Build the production binary.
build:
    go build ./cmd/costume-tree

# Build the OCI image. Override IMAGE=... when needed.
image:
    docker build --tag {{image}} .


# Build and push an image. Set GHCR visibility in the package settings.
docker-push:
    @docker build --quiet --tag {{image_repository}}:{{image_tag}} . >/dev/null
    @docker push {{image_repository}}:{{image_tag}} >&2
    @printf '%s:%s\n' '{{image_repository}}' '{{image_tag}}'

# Build and start the Compose service in the background.
compose-start:
    docker compose up --build --detach

# Stop the Compose service while preserving its named database volume.
compose-stop:
    docker compose down

# Follow Compose service logs.
compose-logs:
    docker compose logs --follow costume-tree

# Create the persistent database and photo volumes, then start a foreground container.
up: image
    @docker volume create {{volume}} >/dev/null
    @docker volume create {{photo_volume}} >/dev/null
    @docker rm --force {{container}} >/dev/null 2>&1 || true
    docker run --rm --name {{container}} --publish {{port}}:8080 --env COSTUMETREE_DIR=/photos --volume {{volume}}:/data --volume {{photo_volume}}:/photos {{image}}

# Start a detached container with persistent database and photo volumes.
start: image
    @docker volume create {{volume}} >/dev/null
    @docker volume create {{photo_volume}} >/dev/null
    @docker rm --force {{container}} >/dev/null 2>&1 || true
    docker run --detach --name {{container}} --publish {{port}}:8080 --env COSTUMETREE_DIR=/photos --volume {{volume}}:/data --volume {{photo_volume}}:/photos {{image}}

# Stop and remove the managed container; data remains in VOLUME.
stop:
    @docker stop {{container}} >/dev/null 2>&1 || true
    @docker rm {{container}} >/dev/null 2>&1 || true

# Follow managed container logs.
logs:
    docker logs --follow {{container}}

# Check the local health endpoint.
health:
    curl --fail --silent --show-error http://127.0.0.1:{{port}}/healthz

# Open a shell in the managed container.
shell:
    docker exec --interactive --tty {{container}} /bin/sh

# Create a validated backup in backups/NAME and leave the volume untouched.
backup name="costume-tree-backup.db":
    @mkdir -p backups
    docker exec {{container}} /costume-tree backup /data/{{name}}
    docker cp {{container}}:/data/{{name}} backups/{{name}}
    @printf 'backup: backups/%s\n' '{{name}}'

# Restore a backup into a new volume.
restore backup destination="restored.db" restore_volume="costume-tree-restore":
    @docker volume create {{restore_volume}} >/dev/null
    docker run --rm --user 0 --entrypoint /bin/sh --volume "$(pwd)/{{backup}}:/source/backup.db:ro" --volume {{restore_volume}}:/data {{image}} -c "cp /source/backup.db /data/.restore-input.db && chown 65532:65532 /data/.restore-input.db && /costume-tree restore /data/.restore-input.db /data/{{destination}} && rm -f /data/.restore-input.db"
    @printf 'restored: volume=%s path=/data/%s\n' '{{restore_volume}}' '{{destination}}'

# Remove the managed container and its persistent volumes.
clean:
    @docker stop {{container}} >/dev/null 2>&1 || true
    @docker rm {{container}} >/dev/null 2>&1 || true
    @docker volume rm {{volume}} >/dev/null 2>&1 || true
    @docker volume rm {{photo_volume}} >/dev/null 2>&1 || true
