# -*- mode: Python -*-

# Docker prune settings for cleanup delete
cache_days = 1
docker_prune_settings(disable=False, max_age_mins=60*24*cache_days, num_builds=0, interval_hrs=1, keep_recent=3)

# Max paralel tasks (also counts builds)
update_settings(max_parallel_updates=3)

docker_compose("./docker-compose/requirements.yaml")
docker_compose("./docker-compose/server.yaml")
docker_compose("./docker-compose/worker.yaml")

dc_resource('postgres', labels=['database'])
dc_resource('pgadmin', labels=['database'])
dc_resource('rabbitmq', labels=['broker'])

include("./Tiltfile.server")
include("./Tiltfile.worker")
include("./Tiltfile.local_resources")
