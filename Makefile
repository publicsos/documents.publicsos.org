#!/bin/sh -l

# Define variables
IMAGE_PROD=izdrail/documents.publicsos.org:main
DOCKERFILE=Dockerfile
DOCKER_COMPOSE_FILE=docker-compose.yaml


build:
	docker image rm -f $(IMAGE_PROD) || true
	docker buildx build \
		--platform linux/amd64 \
		-t $(IMAGE_PROD) \
		--no-cache \
		--progress=plain \
		--build-arg CACHEBUST=$$(date +%s) \
		-f $(DOCKERFILE) \
		.  # <-- Build Context Docker file is located at root


prod:
	docker-compose -f $(DOCKER_COMPOSE_FILE) up --remove-orphans

down:
	docker-compose -f $(DOCKER_COMPOSE_FILE) down

ssh:
	docker exec -it documents.publicsos.org /bin/bash


publish-prod:
	docker push $(IMAGE_PROD)


# Additional functionality
test:
	docker exec documents.publicsos.org php artisan test

migrate:
	docker exec documents.publicsos.org php artisan migrate --force

seed:
	docker exec documents.publicsos.org php artisan db:seed --force

clean-queue:
	docker exec documents.publicsos.org php artisan horizon:clear

lint:
	docker exec documents.publicsos.org ./vendor/bin/phpcs --standard=PSR12 app/

fix-lint:
	docker exec documents.publicsos.org ./vendor/bin/phpcbf --standard=PSR12 app/

prune:
	docker system prune -f --volumes

logs:
	docker logs -f documents.publicsos.org

restart:
	docker-compose -f $(DOCKER_COMPOSE_FILE) down
	docker-compose -f $(DOCKER_COMPOSE_FILE) up --remove-orphans -d

# Cleanup target
clean:
	-docker-compose -f $(DOCKER_COMPOSE_FILE) down --rmi all --volumes --remove-orphans
	-docker system prune -f --volumes
