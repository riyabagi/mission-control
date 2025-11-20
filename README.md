Mission Control Project (Go + RabbitMQ + Redis + React)

Mission Control is a distributed mission management system implemented in Golang, designed to simulate secure military-style command operations. The system allows the Commander (API) to issue missions, queue them asynchronously, and monitor real-time progress as worker units execute tasks on the Battlefield.

The architecture uses:
Go (Golang) for the Commander API and Worker Services
RabbitMQ for workload distribution
Redis for mission persistence and status tracking
React for mission visualization
Docker Compose for full-service containerization and scaling
