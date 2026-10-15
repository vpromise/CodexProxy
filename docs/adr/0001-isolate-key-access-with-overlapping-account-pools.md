# Isolate key access with overlapping account pools

For isolated access, each client API key binds to one independently named account pool belonging to exactly one upstream provider; every selection and retry stays within that pool. Multiple keys may share a pool, and pools may overlap across users, allowing deliberate account sharing while keeping each key's routing permissions explicit. Consequently, users sharing an upstream account also share its upstream quota and rate-limit effects.

Named or annotated client API keys are the first version's access-management unit, keeping administration close to the existing key model without introducing a separate user, login, or role system. The management UI supports creating pools, selecting their accounts, and assigning keys to pools, with changes persisted in configuration.

Client model catalogs are restricted to models supported by the assigned pool, and explicitly requesting an unauthorized model is also rejected. Temporary quota exhaustion or cooldown keeps otherwise authorized models visible, preserving the existing distinction between model visibility and immediate execution availability.

A referenced pool cannot be deleted until its keys are unbound or reassigned. Empty pools may be retained for configuration preparation; a bound key receives an error when its pool is empty or has no usable account, with no fallback outside the pool.
