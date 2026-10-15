# Enable isolation explicitly for existing installations

Account-pool isolation is an explicit opt-in setting that defaults to the existing access behavior, allowing administrators to prepare pool assignments before changing access for existing clients. Before activation, the management UI lists keys without pool assignments; once isolation is enabled, those keys are denied access. This preserves upgrade compatibility while making enabled isolation enforce explicit grants rather than treating missing assignments as unrestricted access.
