import * as THREE from 'three';

export interface VehicleDims {
  length: number;
  width: number;
  height: number;
}

export const dims: VehicleDims = {
  length: 5.2,
  width: 2.0,
  height: 1.75,
};

// Footprint z: -2.6..+2.6. Hood +1.15..+2.6, cab -0.3..+1.15, open bed behind.
const BODY_WIDTH = 1.9;
const FLOOR_Y = 0.45; // raised stance: body floor sits well above the wheels
const HOOD_Y = 0.9; // hood line; the bed floor continues rearward at this height
const RAIL_Y = 1.2; // bed rail top, continues as the cab beltline
const ROOF_Y = 1.75;
const WHEEL_X = 0.87;
const WHEEL_Z = 1.55;
const WHEEL_RADIUS = 0.38;
const WHEEL_WIDTH = 0.26;
const HUB_RADIUS = 0.19;

export function build(): THREE.Group {
  const root = new THREE.Group();

  const bodyMaterial = new THREE.MeshStandardMaterial({
    color: 0x5f8dff,
    flatShading: true,
  });

  const cabinMaterial = new THREE.MeshStandardMaterial({
    color: 0x1d2950,
    flatShading: true,
  });

  const chassisMaterial = new THREE.MeshStandardMaterial({
    color: 0x0a1230,
    flatShading: true,
  });

  const wheelMaterial = new THREE.MeshStandardMaterial({
    color: 0x101018,
    flatShading: true,
  });

  const hubMaterial = new THREE.MeshStandardMaterial({
    color: 0x5c6ba8,
    flatShading: true,
  });

  const headlightMaterial = new THREE.MeshStandardMaterial({
    color: 0xfff3c4,
    emissive: 0xfff3c4,
    flatShading: true,
  });

  const tailLightMaterial = new THREE.MeshStandardMaterial({
    color: 0xe5484d,
    emissive: 0xe5484d,
    flatShading: true,
  });

  // Chassis / underbody slab.
  const chassisGeo = new THREE.BoxGeometry(1.7, 0.16, 4.9);
  const chassis = new THREE.Mesh(chassisGeo, chassisMaterial);
  chassis.position.y = 0.37;
  chassis.name = 'chassis';
  root.add(chassis);

  // Main body: extruded side profile — hood, raked-windshield cab, and bed
  // floor slab are one mesh (the renderer tints this mesh per vehicle).
  const bodyShape = new THREE.Shape();
  bodyShape.moveTo(2.6, FLOOR_Y);
  bodyShape.lineTo(2.6, HOOD_Y);
  bodyShape.lineTo(1.15, HOOD_Y);
  bodyShape.lineTo(0.85, ROOF_Y);
  bodyShape.lineTo(-0.3, ROOF_Y);
  bodyShape.lineTo(-0.3, HOOD_Y);
  bodyShape.lineTo(-2.6, HOOD_Y);
  bodyShape.lineTo(-2.6, FLOOR_Y);
  bodyShape.closePath();

  const bodyGeo = new THREE.ExtrudeGeometry(bodyShape, {
    depth: BODY_WIDTH,
    bevelEnabled: false,
  });
  bodyGeo.rotateY(-Math.PI / 2);
  bodyGeo.translate(BODY_WIDTH / 2, 0, 0);

  const body = new THREE.Mesh(bodyGeo, bodyMaterial);
  body.name = 'body';
  root.add(body);

  // Cabin: glass band wrapping the cab, slightly proud of its sides and
  // back, plus a windshield slab laid on the raked cab front.
  const cabin = new THREE.Group();
  cabin.name = 'cabin';

  const bandGeo = new THREE.BoxGeometry(1.94, 0.42, 1.19);
  const band = new THREE.Mesh(bandGeo, cabinMaterial);
  band.position.set(0, (RAIL_Y + 1.62) / 2, 0.245);
  cabin.add(band);

  const windshieldGeo = new THREE.BoxGeometry(1.7, 0.62, 0.04);
  windshieldGeo.rotateX(-Math.atan(0.3 / 0.85));
  const windshield = new THREE.Mesh(windshieldGeo, cabinMaterial);
  windshield.position.set(0, 1.33, 1.02);
  cabin.add(windshield);

  root.add(cabin);

  // Open cargo bed: dark U-channel (floor + low side rails) plus tailgate.
  // Deliberately "bedliner" dark so the open tub reads in any tint state.
  const bedShape = new THREE.Shape();
  bedShape.moveTo(-0.96, 0.88);
  bedShape.lineTo(-0.96, RAIL_Y);
  bedShape.lineTo(-0.83, RAIL_Y);
  bedShape.lineTo(-0.83, 0.94);
  bedShape.lineTo(0.83, 0.94);
  bedShape.lineTo(0.83, RAIL_Y);
  bedShape.lineTo(0.96, RAIL_Y);
  bedShape.lineTo(0.96, 0.88);
  bedShape.closePath();

  const bedGeo = new THREE.ExtrudeGeometry(bedShape, {
    depth: 2.24,
    bevelEnabled: false,
  });
  bedGeo.translate(0, 0, -2.58); // spans z -2.58..-0.34; front end inside the cab

  const bed = new THREE.Mesh(bedGeo, chassisMaterial);
  root.add(bed);

  const tailgateGeo = new THREE.BoxGeometry(1.92, 0.28, 0.08);
  const tailgate = new THREE.Mesh(tailgateGeo, chassisMaterial);
  tailgate.position.set(0, 1.06, -2.56);
  root.add(tailgate);

  // Wheels.
  const wheels = new THREE.Group();
  wheels.name = 'wheels';

  const wheelGeo = new THREE.CylinderGeometry(
    WHEEL_RADIUS,
    WHEEL_RADIUS,
    WHEEL_WIDTH,
    10
  );
  wheelGeo.rotateZ(Math.PI / 2);

  const hubGeo = new THREE.CircleGeometry(HUB_RADIUS, 8);

  const wheelPositions = [
    { x: -WHEEL_X, z: -WHEEL_Z },
    { x:  WHEEL_X, z: -WHEEL_Z },
    { x: -WHEEL_X, z:  WHEEL_Z },
    { x:  WHEEL_X, z:  WHEEL_Z },
  ];

  for (const pos of wheelPositions) {
    const wheel = new THREE.Mesh(wheelGeo, wheelMaterial);
    wheel.position.set(pos.x, WHEEL_RADIUS, pos.z);
    wheels.add(wheel);

    const outward = pos.x > 0 ? 1 : -1;
    const hub = new THREE.Mesh(hubGeo, hubMaterial);
    hub.position.set(
      pos.x + outward * (WHEEL_WIDTH / 2 + 0.005),
      WHEEL_RADIUS,
      pos.z
    );
    hub.rotation.y = (outward * Math.PI) / 2;
    wheels.add(hub);
  }

  root.add(wheels);

  // Front lights.
  const lightsFront = new THREE.Group();
  lightsFront.name = 'lights-front';

  const headlightGeo = new THREE.BoxGeometry(0.3, 0.12, 0.06);

  const headlightL = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightL.position.set(-0.62, 0.72, dims.length / 2 - 0.03);

  const headlightR = new THREE.Mesh(headlightGeo, headlightMaterial);
  headlightR.position.set(0.62, 0.72, dims.length / 2 - 0.03);

  lightsFront.add(headlightL, headlightR);
  root.add(lightsFront);

  // Rear lights: vertical clusters at the tailgate corners.
  const lightsRear = new THREE.Group();
  lightsRear.name = 'lights-rear';

  const tailLightGeo = new THREE.BoxGeometry(0.1, 0.3, 0.06);

  const tailLightL = new THREE.Mesh(tailLightGeo, tailLightMaterial);
  tailLightL.position.set(-0.82, 1.06, -dims.length / 2 + 0.03);

  const tailLightR = new THREE.Mesh(tailLightGeo, tailLightMaterial);
  tailLightR.position.set(0.82, 1.06, -dims.length / 2 + 0.03);

  lightsRear.add(tailLightL, tailLightR);
  root.add(lightsRear);

  return root;
}
