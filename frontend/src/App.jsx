import React, { useState, useEffect } from "react";

function App() {
  const [payload, setPayload] = useState("");
  const [target, setTarget] = useState("soldier-1");
  const [missionId, setMissionId] = useState("");
  const [statusResult, setStatusResult] = useState(null);
  const [loading, setLoading] = useState(false);
  const [allMissions, setAllMissions] = useState([]);
  const [commander, setCommander] = useState("commander-1");

  const commanders = ["commander-1", "commander-2", "commander-3"];
  const commanderUrl =
    import.meta.env.VITE_COMMANDER_URL || "http://localhost:8080";

  // ---------------- SUBMIT MISSION ----------------
  const submitMission = async () => {
    try {
      setLoading(true);

      const p = payload ? JSON.parse(payload) : { text: "Attack" };

      const res = await fetch(`${commanderUrl}/missions`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          commander_id: commander,
          target,
          payload: p,
        }),
      });

      const j = await res.json();
      setMissionId(j.mission_id || "");

      fetchAllMissions();
    } catch (err) {
      alert("Invalid JSON payload");
    } finally {
      setLoading(false);
    }
  };

  // ---------------- FETCH SINGLE MISSION ----------------
  const fetchStatus = async () => {
    if (!missionId) return;

    setLoading(true);

    const res = await fetch(`${commanderUrl}/missions/${missionId}`);
    const j = await res.json();

    setStatusResult(j);
    fetchAllMissions();
    setLoading(false);
  };

  // ---------------- FETCH ALL MISSIONS ----------------
  const fetchAllMissions = async () => {
    const res = await fetch(
      `${commanderUrl}/missions?commander_id=${encodeURIComponent(commander)}`
    );

    const data = await res.json();
    setAllMissions(data);
  };

  useEffect(() => {
    fetchAllMissions();

    const interval = setInterval(fetchAllMissions, 3000);

    return () => clearInterval(interval);
  }, [commander]);

  return (
    <div className="container py-4">
      <h1 className="text-center mb-4 fw-bold">Mission Commander Dashboard</h1>

      <div className="row">
        
        {/* LEFT SIDE */}
        <div className="col-md-6 mb-4">

          {/* CREATE MISSION CARD */}
          <div className="card shadow-sm mb-4">
            <div className="card-header bg-primary text-white">
              <h5 className="mb-0">Create New Mission</h5>
            </div>

            <div className="card-body">

              {/* COMMANDER SELECT */}
              <label className="form-label fw-semibold">Commander</label>
              <select
                className="form-select mb-3"
                value={commander}
                onChange={(e) => setCommander(e.target.value)}
              >
                {commanders.map((cmd) => (
                  <option key={cmd} value={cmd}>
                    {cmd}
                  </option>
                ))}
              </select>

              {/* TARGET SOLDIER */}
              <label className="form-label fw-semibold">Target Soldier</label>
              <select
                className="form-select mb-3"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
              >
                <option value="soldier-1">soldier-1</option>
                <option value="soldier-2">soldier-2</option>
                <option value="soldier-3">soldier-3</option>
              </select>

              {/* PAYLOAD */}
              <label className="form-label fw-semibold">Mission Payload (JSON)</label>
              <textarea
                className="form-control"
                rows={4}
                value={payload}
                onChange={(e) => setPayload(e.target.value)}
                placeholder='{"task": "scan", "priority": "high"}'
              />

              <button
                className="btn btn-primary mt-3 w-100"
                onClick={submitMission}
                disabled={loading}
              >
                {loading ? "Submitting..." : "Submit Mission"}
              </button>

              {missionId && (
                <div className="alert alert-success mt-3">
                  <strong>Mission Created!</strong> ID: {missionId}
                </div>
              )}
            </div>
          </div>

          {/* CHECK STATUS CARD */}
          <div className="card shadow-sm">
            <div className="card-header bg-dark text-white">
              <h5 className="mb-0">Check Mission Status</h5>
            </div>

            <div className="card-body">
              <label className="form-label fw-semibold">Mission ID</label>
              <input
                className="form-control"
                value={missionId}
                onChange={(e) => setMissionId(e.target.value)}
                placeholder="Enter Mission ID"
              />

              <button
                className="btn btn-dark w-100 mt-3"
                onClick={fetchStatus}
                disabled={loading}
              >
                {loading ? "Fetching..." : "Get Status"}
              </button>

              {statusResult && (
                <div className="card shadow-sm mt-4">
                  <div className="card-body">
                    <h5 className="card-title">Mission Details</h5>

                    <p><strong>ID:</strong> {statusResult.id}</p>
                    <p><strong>Assigned To:</strong> {statusResult.assigned_to}</p>

                    <p>
                      <strong>Status:</strong>{" "}
                      <span
                        className={`badge ms-2 ${
                          statusResult.status === "COMPLETED"
                            ? "bg-success"
                            : statusResult.status === "FAILED"
                            ? "bg-danger"
                            : statusResult.status === "IN_PROGRESS"
                            ? "bg-warning text-dark"
                            : "bg-secondary"
                        }`}
                      >
                        {statusResult.status}
                      </span>
                    </p>

                    <p>
                      <strong>Created:</strong>{" "}
                      {new Date(statusResult.created_at).toLocaleString()}
                    </p>

                    <p>
                      <strong>In Progress:</strong>{" "}
                      {statusResult.in_progress_at
                        ? new Date(statusResult.in_progress_at).toLocaleString()
                        : "N/A"}
                    </p>

                    <p>
                      <strong>Completed:</strong>{" "}
                      {new Date(statusResult.updated_at).toLocaleString()}
                    </p>

                    <h6 className="mt-3">Payload:</h6>
                    <pre className="bg-light p-3 rounded border">
                      {JSON.stringify(statusResult.payload, null, 2)}
                    </pre>
                  </div>
                </div>
              )}
            </div>
          </div>
        </div>

        {/* RIGHT SIDE — MISSION HISTORY */}
        <div className="col-md-6">
          <div className="card shadow-sm">
            <div className="card-header bg-info text-white">
              <h5 className="mb-0">
                Mission History — <span className="fw-normal">{commander}</span>
              </h5>
            </div>

            <div
              className="card-body"
              style={{ maxHeight: "92vh", overflowY: "auto" }}
            >
              {allMissions.length === 0 ? (
                <p className="text-muted">No missions yet.</p>
              ) : (
                allMissions.map((m) => (
                  <div key={m.id} className="border rounded p-2 mb-2">
                    <div className="d-flex justify-content-between">
                      <strong>ID:</strong> <span>{m.id}</span>
                    </div>

                    <div className="d-flex justify-content-between mt-1">
                      <strong>Assigned To:</strong> <span>{m.assigned_to}</span>
                    </div>

                    <div className="d-flex justify-content-between mt-1">
                      <strong>Status:</strong>
                      <span
                        className={`badge ${
                          m.status === "COMPLETED"
                            ? "bg-success"
                            : m.status === "FAILED"
                            ? "bg-danger"
                            : m.status === "IN_PROGRESS"
                            ? "bg-warning text-dark"
                            : "bg-secondary"
                        }`}
                      >
                        {m.status}
                      </span>
                    </div>

                    <div className="text-muted small mt-1">
                      Updated: {new Date(m.updated_at).toLocaleString()}
                    </div>
                  </div>
                ))
              )}
            </div>
          </div>
        </div>

      </div>
    </div>
  );
}

export default App;
