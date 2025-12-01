{

  "task": "recon",

  "target": "alpha",

  "priority": "high"

}



run test file

./test\_missions.sh



Check logs

docker-compose logs -f worker

docker logs -f mission-control-commander-1

# Scale up commander to 200

const commanders = Array.from({ length: 200 }, (\_, i) => `commander-${i + 1}`);



\# Scale up worker 

docker service scale mission\_worker=200

docker-compose up --scale worker=3



